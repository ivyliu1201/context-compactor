package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

var runtimeTestTime = time.Date(2026, 7, 23, 2, 30, 0, 0, time.UTC)

func TestExecuteHookRoutesSupportedHostsThroughNormalizedEvent(t *testing.T) {
	tests := []struct {
		name       string
		host       Host
		input      string
		wantHost   string
		wantOutput string
	}{
		{
			name:       "codex",
			host:       HostCodex,
			input:      `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"UserPromptSubmit","model":"gpt-5","permission_mode":"default","prompt":"transient prompt"}`,
			wantHost:   "codex-cli",
			wantOutput: `{"continue":true,"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"bounded capsule"}}` + "\n",
		},
		{
			name:       "claude",
			host:       HostClaude,
			input:      `{"session_id":"session-1","prompt_id":"prompt-7","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","permission_mode":"default","hook_event_name":"UserPromptSubmit","prompt":"transient prompt"}`,
			wantHost:   "claude-code",
			wantOutput: `{"continue":true,"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"bounded capsule"}}` + "\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var handled protocol.TransientEvent
			handler := HandlerFunc(func(
				_ context.Context,
				event protocol.TransientEvent,
			) (Result, error) {
				handled = event
				return Result{AdditionalContext: "bounded capsule"}, nil
			})

			var output bytes.Buffer
			err := ExecuteHook(
				context.Background(),
				test.host,
				strings.NewReader(test.input),
				&output,
				runtimeTestTime,
				handler,
			)
			if err != nil {
				t.Fatalf("ExecuteHook() error = %v", err)
			}
			if handled.Kind != protocol.EventUserPrompt || handled.Content != "transient prompt" {
				t.Fatalf("handled event = %+v", handled)
			}
			if handled.Metadata["host"] != test.wantHost {
				t.Fatalf("handled host = %q, want %q", handled.Metadata["host"], test.wantHost)
			}
			if _, exists := handled.Metadata["transcript_path"]; exists {
				t.Fatal("handled event exposed transcript_path in metadata")
			}
			if output.String() != test.wantOutput {
				t.Fatalf("ExecuteHook() output = %q, want %q", output.String(), test.wantOutput)
			}
		})
	}
}

func TestExecuteHookLeavesOutputEmptyBeforeValidatedResponse(t *testing.T) {
	validCodex := `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"UserPromptSubmit","model":"gpt-5","permission_mode":"default","prompt":"transient prompt"}`
	preCompact := `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"PreCompact","model":"gpt-5","trigger":"auto"}`
	handlerError := errors.New("processor unavailable")

	tests := []struct {
		name        string
		host        Host
		input       string
		handler     Handler
		wantError   string
		wantHandled bool
	}{
		{
			name:      "unsupported host",
			host:      Host("unknown"),
			input:     validCodex,
			handler:   successfulHandler(""),
			wantError: "unsupported hook host",
		},
		{
			name:      "invalid hook input",
			host:      HostCodex,
			input:     `{"hook_event_name":"Stop"}`,
			handler:   successfulHandler(""),
			wantError: "unsupported Codex hook event",
		},
		{
			name:  "handler failure",
			host:  HostCodex,
			input: validCodex,
			handler: HandlerFunc(func(
				_ context.Context,
				_ protocol.TransientEvent,
			) (Result, error) {
				return Result{}, handlerError
			}),
			wantError:   handlerError.Error(),
			wantHandled: true,
		},
		{
			name:        "unsupported context output",
			host:        HostCodex,
			input:       preCompact,
			handler:     successfulHandler("not allowed"),
			wantError:   "does not support additional context",
			wantHandled: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := HandlerFunc(func(
				ctx context.Context,
				event protocol.TransientEvent,
			) (Result, error) {
				called = true
				return test.handler.Handle(ctx, event)
			})

			var output bytes.Buffer
			err := ExecuteHook(
				context.Background(),
				test.host,
				strings.NewReader(test.input),
				&output,
				runtimeTestTime,
				handler,
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ExecuteHook() error = %v, want containing %q", err, test.wantError)
			}
			if called != test.wantHandled {
				t.Fatalf("handler called = %t, want %t", called, test.wantHandled)
			}
			if output.Len() != 0 {
				t.Fatalf("ExecuteHook() wrote output before success: %q", output.String())
			}
		})
	}
}

func TestExecuteHookRequiresRuntimeDependencies(t *testing.T) {
	validInput := strings.NewReader(`{"hook_event_name":"Stop"}`)
	handler := successfulHandler("")

	tests := []struct {
		name      string
		ctx       context.Context
		input     io.Reader
		output    io.Writer
		handler   Handler
		wantError string
	}{
		{name: "context", input: validInput, output: &bytes.Buffer{}, handler: handler, wantError: "hook context is required"},
		{name: "input", ctx: context.Background(), output: &bytes.Buffer{}, handler: handler, wantError: "hook input is required"},
		{name: "output", ctx: context.Background(), input: validInput, handler: handler, wantError: "hook output is required"},
		{name: "handler", ctx: context.Background(), input: validInput, output: &bytes.Buffer{}, wantError: "hook handler is required"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ExecuteHook(
				test.ctx,
				HostCodex,
				test.input,
				test.output,
				runtimeTestTime,
				test.handler,
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ExecuteHook() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func successfulHandler(additionalContext string) Handler {
	return HandlerFunc(func(
		_ context.Context,
		_ protocol.TransientEvent,
	) (Result, error) {
		return Result{AdditionalContext: additionalContext}, nil
	})
}
