package claude

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

var hookTestTime = time.Date(2026, 7, 22, 7, 45, 0, 0, time.UTC)

func TestDecodeHookNormalizesSupportedClaudeEvents(t *testing.T) {
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
			input:        `{"session_id":"session-1","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","hook_event_name":"SessionStart","source":"resume","model":"claude-sonnet","agent_type":"reviewer"}`,
			wantName:     EventSessionStart,
			wantKind:     protocol.EventSessionStart,
			wantMetadata: map[string]string{"source": "resume", "model": "claude-sonnet"},
		},
		{
			name:         "user prompt",
			input:        `{"session_id":"session-1","prompt_id":"prompt-7","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","permission_mode":"auto","effort":{"level":"xhigh"},"hook_event_name":"UserPromptSubmit","prompt":"keep this transient"}`,
			wantName:     EventUserPromptSubmit,
			wantKind:     protocol.EventUserPrompt,
			wantContent:  "keep this transient",
			wantMetadata: map[string]string{"prompt_id": "prompt-7", "permission_mode": "auto", "effort_level": "xhigh"},
		},
		{
			name:         "subagent start",
			input:        `{"session_id":"session-1","prompt_id":"prompt-7","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","hook_event_name":"SubagentStart","agent_id":"agent-1","agent_type":"Explore"}`,
			wantName:     EventSubagentStart,
			wantKind:     protocol.EventSubagentStart,
			wantMetadata: map[string]string{"agent_id": "agent-1", "agent_type": "Explore"},
		},
		{
			name:         "pre compact",
			input:        `{"session_id":"session-1","prompt_id":"prompt-7","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","hook_event_name":"PreCompact","trigger":"manual","custom_instructions":"retain the current goal"}`,
			wantName:     EventPreCompact,
			wantKind:     protocol.EventPreCompact,
			wantContent:  "retain the current goal",
			wantMetadata: map[string]string{"trigger": "manual"},
		},
		{
			name:         "post compact",
			input:        `{"session_id":"session-1","prompt_id":"prompt-7","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","hook_event_name":"PostCompact","trigger":"auto","compact_summary":"derived compact summary"}`,
			wantName:     EventPostCompact,
			wantKind:     protocol.EventPostCompact,
			wantContent:  "derived compact summary",
			wantMetadata: map[string]string{"trigger": "auto"},
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
			if decoded.Event.CWD != `C:\repo` || decoded.Event.OccurredAt != hookTestTime {
				t.Fatalf("DecodeHook() event = %+v, want supplied cwd and time", decoded.Event)
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

func TestDecodeHookUsesPromptIDForRetryIdentity(t *testing.T) {
	first := `{"session_id":"session-1","prompt_id":"prompt-7","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","permission_mode":"default","hook_event_name":"UserPromptSubmit","prompt":"first payload"}`
	retry := strings.Replace(first, "first payload", "changed retry payload", 1)

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
	if strings.Contains(decodedFirst.Event.ID, "payload") {
		t.Fatalf("event id contains prompt content: %q", decodedFirst.Event.ID)
	}
}

func TestDecodeHookRejectsInvalidClaudeInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "unknown field",
			input: `{"session_id":"session-1","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","hook_event_name":"SessionStart","source":"startup","extra":true}`,
			want:  "unknown field",
		},
		{
			name:  "missing transcript path",
			input: `{"session_id":"session-1","cwd":"C:\\repo","hook_event_name":"SessionStart","source":"startup"}`,
			want:  "transcript_path is required",
		},
		{
			name:  "invalid permission mode",
			input: `{"session_id":"session-1","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","permission_mode":"manual","hook_event_name":"UserPromptSubmit","prompt":"hello"}`,
			want:  "unsupported permission_mode",
		},
		{
			name:  "empty permission mode",
			input: `{"session_id":"session-1","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","permission_mode":"","hook_event_name":"UserPromptSubmit","prompt":"hello"}`,
			want:  "unsupported permission_mode",
		},
		{
			name:  "invalid effort level",
			input: `{"session_id":"session-1","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","effort":{"level":"ultra"},"hook_event_name":"SessionStart","source":"startup"}`,
			want:  "unsupported effort level",
		},
		{
			name:  "missing compact summary",
			input: `{"session_id":"session-1","transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","hook_event_name":"PostCompact","trigger":"auto"}`,
			want:  "compact_summary is required",
		},
		{
			name:  "null optional prompt id",
			input: `{"session_id":"session-1","prompt_id":null,"transcript_path":"C:\\private.jsonl","cwd":"C:\\repo","hook_event_name":"SessionStart","source":"startup"}`,
			want:  "must be a string",
		},
		{
			name:  "unsupported event",
			input: `{"hook_event_name":"Stop"}`,
			want:  "unsupported Claude hook event",
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

func TestWriteOutputLimitsContextToSupportedEvents(t *testing.T) {
	for _, eventName := range []HookEventName{
		EventSessionStart,
		EventUserPromptSubmit,
		EventSubagentStart,
	} {
		var output bytes.Buffer
		err := WriteOutput(&output, eventName, Output{AdditionalContext: "bounded capsule"})
		if err != nil {
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
		err := WriteOutput(&bytes.Buffer{}, eventName, Output{AdditionalContext: "not allowed"})
		if err == nil {
			t.Fatalf("WriteOutput(%s) accepted additional context", eventName)
		}
	}

	var postCompactOutput bytes.Buffer
	if err := WriteOutput(&postCompactOutput, EventPostCompact, Output{}); err != nil {
		t.Fatalf("WriteOutput(PostCompact, empty) error = %v", err)
	}
	if postCompactOutput.String() != "" {
		t.Fatalf("WriteOutput(PostCompact, empty) = %q", postCompactOutput.String())
	}
}

func TestWriteOutputLimitsBlockingToSupportedEvents(t *testing.T) {
	for _, eventName := range []HookEventName{EventUserPromptSubmit, EventPreCompact} {
		var output bytes.Buffer
		err := WriteOutput(&output, eventName, Output{
			Block:       true,
			BlockReason: "run /compact, then retry",
		})
		if err != nil {
			t.Fatalf("WriteOutput(%s) error = %v", eventName, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
			t.Fatalf("decode output: %v", err)
		}
		if decoded["decision"] != "block" || decoded["reason"] != "run /compact, then retry" {
			t.Fatalf("WriteOutput(%s) = %s", eventName, output.String())
		}
	}

	if err := WriteOutput(
		&bytes.Buffer{},
		EventPostCompact,
		Output{Block: true, BlockReason: "not allowed"},
	); err == nil {
		t.Fatal("WriteOutput(PostCompact) accepted blocking")
	}
	if err := WriteOutput(
		&bytes.Buffer{},
		EventPreCompact,
		Output{Block: true},
	); err == nil {
		t.Fatal("WriteOutput(PreCompact) accepted a block without reason")
	}
	if err := WriteOutput(
		&bytes.Buffer{},
		EventUserPromptSubmit,
		Output{BlockReason: "orphan reason"},
	); err == nil {
		t.Fatal("WriteOutput(UserPromptSubmit) accepted a reason without blocking")
	}
}

func TestHostCapabilitiesKeepClaudeAsCompactionOwner(t *testing.T) {
	capabilities := HostCapabilities()
	if capabilities.HostID != HostID || !capabilities.NativeTranscriptCompaction ||
		capabilities.AllowsAdapterTranscriptCompaction {
		t.Fatalf("HostCapabilities() = %+v", capabilities)
	}
}
