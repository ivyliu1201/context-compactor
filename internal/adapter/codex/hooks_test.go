package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"context-compactor/internal/protocol"
)

var hookTestTime = time.Date(2026, 7, 22, 6, 30, 0, 0, time.UTC)

func TestDecodeHookNormalizesSupportedCodexEvents(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantName     HookEventName
		wantKind     protocol.EventKind
		wantContent  string
		wantMetadata map[string]string
	}{
		{
			name:         "session start",
			input:        `{"session_id":"session-1","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"SessionStart","model":"gpt-5","permission_mode":"default","source":"resume"}`,
			wantName:     EventSessionStart,
			wantKind:     protocol.EventSessionStart,
			wantMetadata: map[string]string{"source": "resume"},
		},
		{
			name:         "user prompt",
			input:        `{"session_id":"session-1","turn_id":"turn-7","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","hook_event_name":"UserPromptSubmit","model":"gpt-5","permission_mode":"acceptEdits","prompt":"keep this transient","agent_id":"agent-1","agent_type":"worker"}`,
			wantName:     EventUserPromptSubmit,
			wantKind:     protocol.EventUserPrompt,
			wantContent:  "keep this transient",
			wantMetadata: map[string]string{"turn_id": "turn-7", "agent_id": "agent-1"},
		},
		{
			name:         "pre compact",
			input:        `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"PreCompact","model":"gpt-5","trigger":"auto"}`,
			wantName:     EventPreCompact,
			wantKind:     protocol.EventPreCompact,
			wantMetadata: map[string]string{"turn_id": "turn-7", "trigger": "auto"},
		},
		{
			name:         "post compact",
			input:        `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"PostCompact","model":"gpt-5","trigger":"manual"}`,
			wantName:     EventPostCompact,
			wantKind:     protocol.EventPostCompact,
			wantMetadata: map[string]string{"turn_id": "turn-7", "trigger": "manual"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := DecodeHook(strings.NewReader(test.input), hookTestTime)
			if err != nil {
				t.Fatalf("DecodeHook() error = %v", err)
			}
			if decoded.Name != test.wantName || decoded.Event.Kind != test.wantKind {
				t.Fatalf("DecodeHook() = %+v, want name %q and kind %q", decoded, test.wantName, test.wantKind)
			}
			if decoded.Event.Content != test.wantContent {
				t.Fatalf("DecodeHook() content = %q, want %q", decoded.Event.Content, test.wantContent)
			}
			if decoded.Event.OccurredAt != hookTestTime || decoded.Event.CWD != `C:\repo` {
				t.Fatalf("DecodeHook() event = %+v, want supplied time and cwd", decoded.Event)
			}
			if decoded.Event.Metadata["host"] != HostID {
				t.Fatalf("DecodeHook() host metadata = %q, want %q", decoded.Event.Metadata["host"], HostID)
			}
			if _, exists := decoded.Event.Metadata["transcript_path"]; exists {
				t.Fatal("DecodeHook() exposed transcript_path in metadata")
			}
			for key, want := range test.wantMetadata {
				if decoded.Event.Metadata[key] != want {
					t.Fatalf("DecodeHook() metadata[%q] = %q, want %q", key, decoded.Event.Metadata[key], want)
				}
			}
		})
	}
}

func TestDecodeHookUsesDeterministicIdentityWithoutPromptPersistence(t *testing.T) {
	first := `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"UserPromptSubmit","model":"gpt-5","permission_mode":"default","prompt":"first prompt"}`
	retry := strings.Replace(first, "first prompt", "changed retry payload", 1)

	decodedFirst, err := DecodeHook(strings.NewReader(first), hookTestTime)
	if err != nil {
		t.Fatalf("DecodeHook(first) error = %v", err)
	}
	decodedRetry, err := DecodeHook(strings.NewReader(retry), hookTestTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("DecodeHook(retry) error = %v", err)
	}
	if decodedFirst.Event.ID != decodedRetry.Event.ID {
		t.Fatalf("retry event ids differ: %q != %q", decodedFirst.Event.ID, decodedRetry.Event.ID)
	}
	if strings.Contains(decodedFirst.Event.ID, "prompt") {
		t.Fatalf("event id contains prompt content: %q", decodedFirst.Event.ID)
	}
}

func TestDecodeHookRejectsInvalidCodexInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "unknown field",
			input: `{"session_id":"session-1","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"SessionStart","model":"gpt-5","permission_mode":"default","source":"startup","extra":true}`,
			want:  "unknown field",
		},
		{
			name:  "missing nullable transcript path",
			input: `{"session_id":"session-1","cwd":"C:\\repo","hook_event_name":"SessionStart","model":"gpt-5","permission_mode":"default","source":"startup"}`,
			want:  "transcript_path is required",
		},
		{
			name:  "invalid trigger",
			input: `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"PreCompact","model":"gpt-5","trigger":"scheduled"}`,
			want:  "unsupported trigger",
		},
		{
			name:  "field from another hook schema",
			input: `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"PreCompact","model":"gpt-5","trigger":"auto","prompt":"not allowed"}`,
			want:  "unknown field",
		},
		{
			name:  "missing prompt",
			input: `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"UserPromptSubmit","model":"gpt-5","permission_mode":"default"}`,
			want:  "prompt is required",
		},
		{
			name:  "null optional agent id",
			input: `{"session_id":"session-1","turn_id":"turn-7","transcript_path":null,"cwd":"C:\\repo","hook_event_name":"PreCompact","model":"gpt-5","trigger":"auto","agent_id":null}`,
			want:  "must be a string",
		},
		{
			name:  "unsupported event",
			input: `{"hook_event_name":"Stop"}`,
			want:  "unsupported Codex hook event",
		},
		{
			name:  "trailing JSON",
			input: `{"hook_event_name":"Stop"}{}`,
			want:  "exactly one JSON value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeHook(strings.NewReader(test.input), hookTestTime)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeHook() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestWriteOutputUsesOnlySupportedAdditionalContextEvents(t *testing.T) {
	for _, eventName := range []HookEventName{EventSessionStart, EventUserPromptSubmit} {
		var output bytes.Buffer
		if err := WriteOutput(&output, eventName, "bounded capsule"); err != nil {
			t.Fatalf("WriteOutput(%s) error = %v", eventName, err)
		}
		var decoded struct {
			Continue           bool `json:"continue"`
			HookSpecificOutput struct {
				HookEventName     HookEventName `json:"hookEventName"`
				AdditionalContext string        `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
			t.Fatalf("decode output: %v", err)
		}
		if !decoded.Continue || decoded.HookSpecificOutput.HookEventName != eventName ||
			decoded.HookSpecificOutput.AdditionalContext != "bounded capsule" {
			t.Fatalf("WriteOutput(%s) = %s", eventName, output.String())
		}
	}

	for _, eventName := range []HookEventName{EventPreCompact, EventPostCompact} {
		var output bytes.Buffer
		if err := WriteOutput(&output, eventName, "bounded capsule"); err == nil {
			t.Fatalf("WriteOutput(%s) accepted additional context", eventName)
		}
		if err := WriteOutput(&output, eventName, ""); err != nil {
			t.Fatalf("WriteOutput(%s, empty) error = %v", eventName, err)
		}
		if output.String() != "{\"continue\":true}\n" {
			t.Fatalf("WriteOutput(%s, empty) = %q", eventName, output.String())
		}
	}
}

func TestHostCapabilitiesKeepCodexAsCompactionOwner(t *testing.T) {
	capabilities := HostCapabilities()
	if capabilities.HostID != HostID || !capabilities.NativeTranscriptCompaction ||
		capabilities.AllowsAdapterTranscriptCompaction {
		t.Fatalf("HostCapabilities() = %+v", capabilities)
	}
	if capabilities.HostID != "codex-cli" {
		t.Fatalf("HostCapabilities() host id = %q", capabilities.HostID)
	}
}
