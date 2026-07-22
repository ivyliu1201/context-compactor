// Package codex translates Codex CLI hook JSON into the stable adapter
// protocol without reading or persisting Codex transcripts.
package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	coreadapter "context-compactor/internal/adapter"
	"context-compactor/internal/protocol"
)

const HostID = "codex-cli"

type HookEventName string

const (
	EventSessionStart     HookEventName = "SessionStart"
	EventUserPromptSubmit HookEventName = "UserPromptSubmit"
	EventPreCompact       HookEventName = "PreCompact"
	EventPostCompact      HookEventName = "PostCompact"
)

type DecodedHook struct {
	Name  HookEventName
	Event protocol.TransientEvent
}

// HostCapabilities declares Codex as the sole transcript compaction owner.
// The context-compactor adapter only supplies bounded additional context.
func HostCapabilities() coreadapter.HostCapabilities {
	return coreadapter.HostCapabilities{
		HostID:                     HostID,
		NativeTranscriptCompaction: true,
	}
}

// DecodeHook strictly decodes one supported Codex command-hook payload. The
// caller supplies receipt time because Codex hook inputs do not include a
// timestamp. Complete prompts remain only in the returned transient event.
func DecodeHook(reader io.Reader, occurredAt time.Time) (DecodedHook, error) {
	raw, err := decodeOneJSON(reader)
	if err != nil {
		return DecodedHook{}, fmt.Errorf("decode Codex hook: %w", err)
	}

	var route struct {
		HookEventName HookEventName `json:"hook_event_name"`
	}
	if err := json.Unmarshal(raw, &route); err != nil {
		return DecodedHook{}, fmt.Errorf("decode Codex hook event name: %w", err)
	}

	var decoded DecodedHook
	switch route.HookEventName {
	case EventSessionStart:
		decoded, err = decodeSessionStart(raw, occurredAt)
	case EventUserPromptSubmit:
		decoded, err = decodeUserPromptSubmit(raw, occurredAt)
	case EventPreCompact, EventPostCompact:
		decoded, err = decodeCompact(raw, occurredAt, route.HookEventName)
	default:
		return DecodedHook{}, fmt.Errorf(
			"unsupported Codex hook event %q",
			route.HookEventName,
		)
	}
	if err != nil {
		return DecodedHook{}, fmt.Errorf("decode %s hook: %w", route.HookEventName, err)
	}
	if err := protocol.ValidateTransientEvent(decoded.Event); err != nil {
		return DecodedHook{}, fmt.Errorf("validate %s hook: %w", route.HookEventName, err)
	}
	return decoded, nil
}

// WriteOutput writes the JSON object Codex expects on hook stdout. Only
// SessionStart and UserPromptSubmit support additionalContext.
func WriteOutput(writer io.Writer, eventName HookEventName, additionalContext string) error {
	if writer == nil {
		return fmt.Errorf("Codex hook output writer is required")
	}

	output := hookOutput{Continue: true}
	switch eventName {
	case EventSessionStart, EventUserPromptSubmit:
		if additionalContext != "" {
			output.HookSpecificOutput = &hookSpecificOutput{
				HookEventName:     eventName,
				AdditionalContext: additionalContext,
			}
		}
	case EventPreCompact, EventPostCompact:
		if additionalContext != "" {
			return fmt.Errorf("Codex %s hook does not support additional context", eventName)
		}
	default:
		return fmt.Errorf("unsupported Codex hook event %q", eventName)
	}

	if err := json.NewEncoder(writer).Encode(output); err != nil {
		return fmt.Errorf("encode Codex %s hook output: %w", eventName, err)
	}
	return nil
}

type sessionStartInput struct {
	SessionID      string         `json:"session_id"`
	TranscriptPath nullableString `json:"transcript_path"`
	CWD            string         `json:"cwd"`
	HookEventName  HookEventName  `json:"hook_event_name"`
	Model          string         `json:"model"`
	PermissionMode string         `json:"permission_mode"`
	Source         string         `json:"source"`
}

type userPromptSubmitInput struct {
	SessionID      string         `json:"session_id"`
	TurnID         string         `json:"turn_id"`
	TranscriptPath nullableString `json:"transcript_path"`
	CWD            string         `json:"cwd"`
	HookEventName  HookEventName  `json:"hook_event_name"`
	Model          string         `json:"model"`
	PermissionMode string         `json:"permission_mode"`
	Prompt         requiredString `json:"prompt"`
	AgentID        optionalString `json:"agent_id,omitempty"`
	AgentType      optionalString `json:"agent_type,omitempty"`
}

type compactInput struct {
	SessionID      string         `json:"session_id"`
	TurnID         string         `json:"turn_id"`
	TranscriptPath nullableString `json:"transcript_path"`
	CWD            string         `json:"cwd"`
	HookEventName  HookEventName  `json:"hook_event_name"`
	Model          string         `json:"model"`
	Trigger        string         `json:"trigger"`
	AgentID        optionalString `json:"agent_id,omitempty"`
	AgentType      optionalString `json:"agent_type,omitempty"`
}

type nullableString struct {
	present bool
}

func (value *nullableString) UnmarshalJSON(data []byte) error {
	value.present = true
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	var decoded string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	return nil
}

type requiredString struct {
	value   string
	present bool
}

func (value *requiredString) UnmarshalJSON(data []byte) error {
	value.present = true
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("must be a string")
	}
	return json.Unmarshal(data, &value.value)
}

type optionalString string

func (value *optionalString) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("must be a string")
	}
	return json.Unmarshal(data, (*string)(value))
}

type hookOutput struct {
	Continue           bool                `json:"continue"`
	HookSpecificOutput *hookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type hookSpecificOutput struct {
	HookEventName     HookEventName `json:"hookEventName"`
	AdditionalContext string        `json:"additionalContext"`
}

func decodeSessionStart(raw []byte, occurredAt time.Time) (DecodedHook, error) {
	var input sessionStartInput
	if err := decodeStrict(raw, &input); err != nil {
		return DecodedHook{}, err
	}
	if err := requireCommon(
		input.SessionID,
		input.CWD,
		input.Model,
		input.HookEventName,
		EventSessionStart,
		input.TranscriptPath.present,
	); err != nil {
		return DecodedHook{}, err
	}
	if !oneOf(input.PermissionMode, "default", "acceptEdits", "plan", "dontAsk", "bypassPermissions") {
		return DecodedHook{}, fmt.Errorf("unsupported permission_mode %q", input.PermissionMode)
	}
	if !oneOf(input.Source, "startup", "resume", "clear", "compact") {
		return DecodedHook{}, fmt.Errorf("unsupported source %q", input.Source)
	}

	metadata := map[string]string{
		"host":            HostID,
		"hook_event_name": string(input.HookEventName),
		"model":           input.Model,
		"permission_mode": input.PermissionMode,
		"source":          input.Source,
	}
	return DecodedHook{
		Name: input.HookEventName,
		Event: newTransientEvent(
			input.SessionID,
			input.CWD,
			protocol.EventSessionStart,
			occurredAt,
			metadata,
			input.Source,
		),
	}, nil
}

func decodeUserPromptSubmit(raw []byte, occurredAt time.Time) (DecodedHook, error) {
	var input userPromptSubmitInput
	if err := decodeStrict(raw, &input); err != nil {
		return DecodedHook{}, err
	}
	if err := requireTurn(
		input.SessionID,
		input.TurnID,
		input.CWD,
		input.Model,
		input.HookEventName,
		EventUserPromptSubmit,
		input.TranscriptPath.present,
	); err != nil {
		return DecodedHook{}, err
	}
	if !oneOf(input.PermissionMode, "default", "acceptEdits", "plan", "dontAsk", "bypassPermissions") {
		return DecodedHook{}, fmt.Errorf("unsupported permission_mode %q", input.PermissionMode)
	}
	if !input.Prompt.present {
		return DecodedHook{}, fmt.Errorf("prompt is required")
	}

	metadata := turnMetadata(
		input.HookEventName,
		input.Model,
		input.TurnID,
		string(input.AgentID),
		string(input.AgentType),
	)
	metadata["permission_mode"] = input.PermissionMode
	event := newTransientEvent(
		input.SessionID,
		input.CWD,
		protocol.EventUserPrompt,
		occurredAt,
		metadata,
		input.TurnID,
		string(input.AgentID),
		string(input.AgentType),
	)
	event.Content = input.Prompt.value
	return DecodedHook{Name: input.HookEventName, Event: event}, nil
}

func decodeCompact(
	raw []byte,
	occurredAt time.Time,
	want HookEventName,
) (DecodedHook, error) {
	var input compactInput
	if err := decodeStrict(raw, &input); err != nil {
		return DecodedHook{}, err
	}
	if err := requireTurn(
		input.SessionID,
		input.TurnID,
		input.CWD,
		input.Model,
		input.HookEventName,
		want,
		input.TranscriptPath.present,
	); err != nil {
		return DecodedHook{}, err
	}
	if !oneOf(input.Trigger, "manual", "auto") {
		return DecodedHook{}, fmt.Errorf("unsupported trigger %q", input.Trigger)
	}

	kind := protocol.EventPreCompact
	if want == EventPostCompact {
		kind = protocol.EventPostCompact
	}
	metadata := turnMetadata(
		input.HookEventName,
		input.Model,
		input.TurnID,
		string(input.AgentID),
		string(input.AgentType),
	)
	metadata["trigger"] = input.Trigger
	return DecodedHook{
		Name: input.HookEventName,
		Event: newTransientEvent(
			input.SessionID,
			input.CWD,
			kind,
			occurredAt,
			metadata,
			input.TurnID,
			input.Trigger,
			string(input.AgentID),
			string(input.AgentType),
		),
	}, nil
}

func requireTurn(
	sessionID string,
	turnID string,
	cwd string,
	model string,
	got HookEventName,
	want HookEventName,
	transcriptPathPresent bool,
) error {
	if err := requireCommon(
		sessionID,
		cwd,
		model,
		got,
		want,
		transcriptPathPresent,
	); err != nil {
		return err
	}
	if strings.TrimSpace(turnID) == "" {
		return fmt.Errorf("turn_id is required")
	}
	return nil
}

func requireCommon(
	sessionID string,
	cwd string,
	model string,
	got HookEventName,
	want HookEventName,
	transcriptPathPresent bool,
) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(cwd) == "" {
		return fmt.Errorf("cwd is required")
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("model is required")
	}
	if got != want {
		return fmt.Errorf("hook_event_name must equal %q", want)
	}
	if !transcriptPathPresent {
		return fmt.Errorf("transcript_path is required and may be null")
	}
	return nil
}

func turnMetadata(
	hookEventName HookEventName,
	model string,
	turnID string,
	agentID string,
	agentType string,
) map[string]string {
	metadata := map[string]string{
		"host":            HostID,
		"hook_event_name": string(hookEventName),
		"model":           model,
		"turn_id":         turnID,
	}
	if agentID != "" {
		metadata["agent_id"] = agentID
	}
	if agentType != "" {
		metadata["agent_type"] = agentType
	}
	return metadata
}

func newTransientEvent(
	sessionID string,
	cwd string,
	kind protocol.EventKind,
	occurredAt time.Time,
	metadata map[string]string,
	identityParts ...string,
) protocol.TransientEvent {
	identity := []string{HostID, string(kind), sessionID}
	identity = append(identity, identityParts...)
	digest := sha256.Sum256([]byte(strings.Join(identity, "\x00")))
	return protocol.TransientEvent{
		Protocol:   protocol.Version,
		ID:         "codex-" + hex.EncodeToString(digest[:]),
		SessionID:  sessionID,
		Kind:       kind,
		OccurredAt: occurredAt,
		CWD:        cwd,
		Metadata:   metadata,
	}
}

func decodeOneJSON(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, fmt.Errorf("input reader is required")
	}
	decoder := json.NewDecoder(reader)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("input must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("read trailing input: %w", err)
	}
	return raw, nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
