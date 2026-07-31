package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultCodexExecutable  = "codex"
	defaultClaudeExecutable = "claude"
	defaultReasoningEffort  = "low"

	MemoryWorkerChildEnvironment = "CONTEXT_COMPACTOR_MEMORY_WORKER_CHILD"

	codexMemoryOutputSchema = `{
  "type": "object",
  "properties": {
    "protocol": {
      "type": "string",
      "enum": ["context-compactor/v1"]
    },
    "outcome": {
      "type": "string",
      "enum": ["no_change", "memory_update"]
    },
    "memory_update": {
      "anyOf": [
        {
          "type": "object",
          "properties": {
            "protocol": {
              "type": "string",
              "enum": ["context-compactor/v1"]
            },
            "privacy_mode": {
              "type": "string",
              "enum": ["balanced"]
            },
            "source_event_id": {
              "type": "string"
            },
            "created_at": {
              "type": "string"
            },
            "operations": {
              "type": "array",
              "items": {
                "type": "object",
                "properties": {
                  "id": {
                    "type": "string"
                  },
                  "kind": {
                    "type": "string",
                    "enum": ["add", "supersede", "resolve", "expire"]
                  },
                  "target_id": {
                    "type": ["string", "null"]
                  },
                  "record": {
                    "anyOf": [
                      {
                        "type": "object",
                        "properties": {
                          "id": {
                            "type": "string"
                          },
                          "conflict_key": {
                            "type": ["string", "null"]
                          },
                          "kind": {
                            "type": "string",
                            "enum": [
                              "goal",
                              "acceptance_criterion",
                              "constraint",
                              "decision",
                              "blocker",
                              "question",
                              "task",
                              "file",
                              "test_result"
                            ]
                          },
                          "value": {
                            "type": "string"
                          },
                          "priority": {
                            "type": "string",
                            "enum": ["critical", "high", "normal", "low"]
                          },
                          "confidence": {
                            "type": "string",
                            "enum": ["explicit", "verified", "inferred"]
                          },
                          "status": {
                            "type": "string",
                            "enum": ["active"]
                          },
                          "source": {
                            "type": "object",
                            "properties": {
                              "event_id": {
                                "type": "string"
                              },
                              "evidence": {
                                "type": ["string", "null"]
                              },
                              "artifact": {
                                "type": ["string", "null"]
                              }
                            },
                            "required": ["event_id", "evidence", "artifact"],
                            "additionalProperties": false
                          },
                          "created_at": {
                            "type": "string"
                          },
                          "expires_at": {
                            "type": ["string", "null"]
                          }
                        },
                        "required": [
                          "id",
                          "conflict_key",
                          "kind",
                          "value",
                          "priority",
                          "confidence",
                          "status",
                          "source",
                          "created_at",
                          "expires_at"
                        ],
                        "additionalProperties": false
                      },
                      {
                        "type": "null"
                      }
                    ]
                  }
                },
                "required": ["id", "kind", "target_id", "record"],
                "additionalProperties": false
              }
            }
          },
          "required": [
            "protocol",
            "privacy_mode",
            "source_event_id",
            "created_at",
            "operations"
          ],
          "additionalProperties": false
        },
        {
          "type": "null"
        }
      ]
    }
  },
  "required": ["protocol", "outcome", "memory_update"],
  "additionalProperties": false
}`
)

// HostModelRunner invokes the same signed-in host CLI that delivered the hook.
// Executable fields are injectable so tests and managed installations do not
// need shell command construction.
type HostModelRunner struct {
	CodexExecutable    string
	ClaudeExecutable   string
	CodexReasoning     string
	UseAnthropicAPIKey bool
}

func (runner HostModelRunner) Invoke(
	ctx context.Context,
	call ModelCall,
) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("model command context is required")
	}
	switch strings.ToLower(strings.TrimSpace(call.Adapter)) {
	case "codex", "codex-cli":
		return runner.invokeCodex(ctx, call)
	case "claude", "claude-code":
		return runner.invokeClaude(ctx, call)
	default:
		return "", fmt.Errorf("unsupported model command adapter %q", call.Adapter)
	}
}

func (runner HostModelRunner) invokeCodex(
	ctx context.Context,
	call ModelCall,
) (string, error) {
	executable := strings.TrimSpace(runner.CodexExecutable)
	if executable == "" {
		executable = defaultCodexExecutable
	}
	reasoning := strings.TrimSpace(runner.CodexReasoning)
	if reasoning == "" {
		reasoning = defaultReasoningEffort
	}

	outputFile, err := os.CreateTemp("", "context-compactor-model-*.json")
	if err != nil {
		return "", fmt.Errorf("create model output file: %w", err)
	}
	outputPath := outputFile.Name()
	if closeErr := outputFile.Close(); closeErr != nil {
		_ = os.Remove(outputPath)
		return "", fmt.Errorf("close model output file: %w", closeErr)
	}
	defer func() { _ = os.Remove(outputPath) }()

	schemaFile, err := os.CreateTemp("", "context-compactor-model-schema-*.json")
	if err != nil {
		return "", fmt.Errorf("create model output schema file: %w", err)
	}
	schemaPath := schemaFile.Name()
	if _, err := schemaFile.WriteString(codexMemoryOutputSchema); err != nil {
		_ = schemaFile.Close()
		_ = os.Remove(schemaPath)
		return "", fmt.Errorf("write model output schema file: %w", err)
	}
	if closeErr := schemaFile.Close(); closeErr != nil {
		_ = os.Remove(schemaPath)
		return "", fmt.Errorf("close model output schema file: %w", closeErr)
	}
	defer func() { _ = os.Remove(schemaPath) }()

	args := codexModelArgs(
		call.Model,
		call.ProjectRoot,
		outputPath,
		schemaPath,
		reasoning,
	)
	command := exec.CommandContext(ctx, executable, args...)
	configureBackgroundModelCommand(command)
	command.Dir = call.ProjectRoot
	command.Stdin = strings.NewReader(call.Prompt)
	command.Env = append(
		os.Environ(),
		MemoryWorkerChildEnvironment+"=1",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("codex model command failed: %w", err)
	}
	output, err := os.ReadFile(filepath.Clean(outputPath))
	if err != nil {
		return "", fmt.Errorf("read codex model output: %w", err)
	}
	if len(output) > maxModelOutputBytes {
		return "", fmt.Errorf("codex model output exceeds %d bytes", maxModelOutputBytes)
	}
	return strings.TrimSpace(string(output)), nil
}

func (runner HostModelRunner) invokeClaude(
	ctx context.Context,
	call ModelCall,
) (string, error) {
	executable := strings.TrimSpace(runner.ClaudeExecutable)
	if executable == "" {
		executable = defaultClaudeExecutable
	}
	args := claudeModelArgs(call.Model, call.Prompt)
	command := exec.CommandContext(ctx, executable, args...)
	configureBackgroundModelCommand(command)
	command.Dir = call.ProjectRoot
	command.Env = append(
		filterAnthropicEnvironment(os.Environ(), runner.UseAnthropicAPIKey),
		MemoryWorkerChildEnvironment+"=1",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("claude model command failed: %w", err)
	}
	if stdout.Len() > maxModelOutputBytes {
		return "", fmt.Errorf("claude model output exceeds %d bytes", maxModelOutputBytes)
	}
	return unwrapClaudeModelOutput(strings.TrimSpace(stdout.String())), nil
}

func codexModelArgs(
	model string,
	projectRoot string,
	outputPath string,
	schemaPath string,
	reasoningEffort string,
) []string {
	args := []string{"exec", "-m", model}
	if reasoningEffort != "" {
		args = append(
			args,
			"-c",
			fmt.Sprintf(`model_reasoning_effort=%q`, reasoningEffort),
		)
	}
	return append(args,
		"--cd", projectRoot,
		"--skip-git-repo-check",
		"--ephemeral",
		"--output-schema", schemaPath,
		"--output-last-message", outputPath,
		"-",
	)
}

func claudeModelArgs(model string, prompt string) []string {
	return []string{
		"--safe-mode",
		"-p",
		"--model",
		model,
		"--output-format",
		"json",
		"--no-session-persistence",
		"--permission-mode",
		"dontAsk",
		"--tools",
		"",
		prompt,
	}
}

func unwrapClaudeModelOutput(output string) string {
	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err == nil &&
		strings.TrimSpace(envelope.Result) != "" {
		return strings.TrimSpace(envelope.Result)
	}
	return output
}

func filterAnthropicEnvironment(environment []string, keepAPIKey bool) []string {
	if keepAPIKey {
		return append([]string(nil), environment...)
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "ANTHROPIC_API_KEY") ||
			strings.EqualFold(name, "CLAUDE_API_KEY") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
