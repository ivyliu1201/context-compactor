package runtime

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCodexModelArgsUseEphemeralConversation(t *testing.T) {
	args := codexModelArgs(
		"gpt-test",
		`C:\repo`,
		`C:\temp\output.json`,
		`C:\temp\schema.json`,
		"low",
	)

	want := []string{
		"exec",
		"-m",
		"gpt-test",
		"-c",
		`model_reasoning_effort="low"`,
		"--cd",
		`C:\repo`,
		"--skip-git-repo-check",
		"--ephemeral",
		"--output-schema",
		`C:\temp\schema.json`,
		"--output-last-message",
		`C:\temp\output.json`,
		"-",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("codexModelArgs() = %#v, want %#v", args, want)
	}
}

func TestCodexMemoryOutputSchemaRejectsUnknownTopLevelFields(t *testing.T) {
	var schema struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal([]byte(codexMemoryOutputSchema), &schema); err != nil {
		t.Fatalf("decode codexMemoryOutputSchema: %v", err)
	}
	if schema.Type != "object" {
		t.Fatalf("schema type = %q, want object", schema.Type)
	}
	if schema.AdditionalProperties {
		t.Fatal("schema allows unknown top-level fields")
	}
	if _, found := schema.Properties["type"]; found {
		t.Fatal("schema unexpectedly declares the rejected top-level type field")
	}
	for _, field := range []string{"protocol", "outcome", "memory_update"} {
		if _, found := schema.Properties[field]; !found {
			t.Fatalf("schema properties do not include %q", field)
		}
		if !containsString(schema.Required, field) {
			t.Fatalf("schema required fields do not include %q", field)
		}
	}
}

func TestClaudeModelArgsDisableToolsAndSessionPersistence(t *testing.T) {
	args := claudeModelArgs("haiku", "bounded prompt")
	joined := strings.Join(args, "\x00")

	for _, required := range []string{
		"--safe-mode",
		"--no-session-persistence",
		"--permission-mode\x00dontAsk",
		"--tools\x00",
		"bounded prompt",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("claude args = %#v, missing %q", args, required)
		}
	}
}

func TestUnwrapClaudeModelOutput(t *testing.T) {
	const result = `{"protocol":"context-compactor/v1","outcome":"no_change"}`
	wrapped := `{"type":"result","result":` + quotedJSON(result) + `}`

	if got := unwrapClaudeModelOutput(wrapped); got != result {
		t.Fatalf("unwrapClaudeModelOutput() = %q, want %q", got, result)
	}
	if got := unwrapClaudeModelOutput(result); got != result {
		t.Fatalf("plain unwrapClaudeModelOutput() = %q", got)
	}
}

func TestFilterAnthropicEnvironmentRemovesCredentialVariables(t *testing.T) {
	environment := []string{
		"PATH=C:\\tools",
		"ANTHROPIC_API_KEY=example",
		"CLAUDE_API_KEY=example",
		"OTHER=value",
	}

	filtered := filterAnthropicEnvironment(environment, false)
	joined := strings.Join(filtered, "\n")
	if strings.Contains(joined, "API_KEY") {
		t.Fatalf("filtered environment = %#v", filtered)
	}
	if !strings.Contains(joined, "PATH=") || !strings.Contains(joined, "OTHER=") {
		t.Fatalf("filtered environment lost safe entries: %#v", filtered)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func quotedJSON(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
	)
	return `"` + replacer.Replace(value) + `"`
}
