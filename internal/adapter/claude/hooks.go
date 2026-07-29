// Package claude translates Claude Code hook JSON into the stable adapter
// protocol without reading or retaining Claude transcript paths.
package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	coreadapter "github.com/ivyliu1201/context-compactor/internal/adapter"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

const HostID = "claude-code"

type HookEventName string

const (
	EventSessionStart     HookEventName = "SessionStart"
	EventUserPromptSubmit HookEventName = "UserPromptSubmit"
	EventSubagentStart    HookEventName = "SubagentStart"
	EventPreCompact       HookEventName = "PreCompact"
	EventPostCompact      HookEventName = "PostCompact"
)

type DecodedHook struct {
	Name  HookEventName
	Event protocol.TransientEvent
}

// Output describes only the context and blocking controls used by the Claude
// adapter. Policy code outside this package decides when a block is needed.
type Output struct {
	AdditionalContext string
	Block             bool
	BlockReason       string
}

// HostCapabilities declares Claude Code as the sole transcript compaction
// owner. The adapter supplies structured context but does not compact twice.
func HostCapabilities() coreadapter.HostCapabilities {
	return coreadapter.HostCapabilities{
		HostID:                     HostID,
		NativeTranscriptCompaction: true,
	}
}

// DecodeHook strictly decodes one supported Claude Code command-hook payload.
// Prompt text, compact instructions, and compact summaries remain transient.
func DecodeHook(reader io.Reader, occurredAt time.Time) (DecodedHook, error) {
	raw, err := decodeOneJSON(reader)
	if err != nil {
		return DecodedHook{}, fmt.Errorf("decode Claude hook: %w", err)
	}

	var route struct {
		HookEventName HookEventName `json:"hook_event_name"`
	}
	if err := json.Unmarshal(raw, &route); err != nil {
		return DecodedHook{}, fmt.Errorf("decode Claude hook event name: %w", err)
	}

	var decoded DecodedHook
	switch route.HookEventName {
	case EventSessionStart:
		decoded, err = decodeSessionStart(raw, occurredAt)
	case EventUserPromptSubmit:
		decoded, err = decodeUserPromptSubmit(raw, occurredAt)
	case EventSubagentStart:
		decoded, err = decodeSubagentStart(raw, occurredAt)
	case EventPreCompact:
		decoded, err = decodeCompact(raw, occurredAt, EventPreCompact)
	case EventPostCompact:
		decoded, err = decodeCompact(raw, occurredAt, EventPostCompact)
	default:
		return DecodedHook{}, fmt.Errorf(
			"unsupported Claude hook event %q",
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

// WriteOutput writes a response only when context or a blocking decision is
// present. A successful hook with no verified memory leaves stdout empty.
func WriteOutput(writer io.Writer, eventName HookEventName, output Output) error {
	if writer == nil {
		return fmt.Errorf("Claude hook output writer is required")
	}
	if err := validateOutput(eventName, output); err != nil {
		return err
	}
	if output.AdditionalContext == "" && !output.Block {
		return nil
	}

	wire := hookOutput{Continue: true}
	if output.Block {
		wire.Decision = "block"
		wire.Reason = output.BlockReason
	}
	if output.AdditionalContext != "" {
		wire.HookSpecificOutput = &hookSpecificOutput{
			HookEventName:     eventName,
			AdditionalContext: output.AdditionalContext,
		}
	}
	if err := json.NewEncoder(writer).Encode(wire); err != nil {
		return fmt.Errorf("encode Claude %s hook output: %w", eventName, err)
	}
	return nil
}

type sessionStartInput struct {
	SessionID      string                 `json:"session_id"`
	PromptID       optionalString         `json:"prompt_id,omitempty"`
	TranscriptPath requiredString         `json:"transcript_path"`
	CWD            string                 `json:"cwd"`
	PermissionMode optionalPermissionMode `json:"permission_mode,omitempty"`
	Effort         optionalEffort         `json:"effort,omitempty"`
	HookEventName  HookEventName          `json:"hook_event_name"`
	Source         string                 `json:"source"`
	Model          optionalString         `json:"model,omitempty"`
	AgentID        optionalString         `json:"agent_id,omitempty"`
	AgentType      optionalString         `json:"agent_type,omitempty"`
	SessionTitle   optionalString         `json:"session_title,omitempty"`
}

type userPromptSubmitInput struct {
	SessionID      string         `json:"session_id"`
	PromptID       optionalString `json:"prompt_id,omitempty"`
	TranscriptPath requiredString `json:"transcript_path"`
	CWD            string         `json:"cwd"`
	PermissionMode requiredString `json:"permission_mode"`
	Effort         optionalEffort `json:"effort,omitempty"`
	HookEventName  HookEventName  `json:"hook_event_name"`
	Prompt         requiredString `json:"prompt"`
	AgentID        optionalString `json:"agent_id,omitempty"`
	AgentType      optionalString `json:"agent_type,omitempty"`
}

type subagentStartInput struct {
	SessionID      string                 `json:"session_id"`
	PromptID       optionalString         `json:"prompt_id,omitempty"`
	TranscriptPath requiredString         `json:"transcript_path"`
	CWD            string                 `json:"cwd"`
	PermissionMode optionalPermissionMode `json:"permission_mode,omitempty"`
	Effort         optionalEffort         `json:"effort,omitempty"`
	HookEventName  HookEventName          `json:"hook_event_name"`
	AgentID        requiredString         `json:"agent_id"`
	AgentType      requiredString         `json:"agent_type"`
}

type preCompactInput struct {
	SessionID          string                 `json:"session_id"`
	PromptID           optionalString         `json:"prompt_id,omitempty"`
	TranscriptPath     requiredString         `json:"transcript_path"`
	CWD                string                 `json:"cwd"`
	PermissionMode     optionalPermissionMode `json:"permission_mode,omitempty"`
	Effort             optionalEffort         `json:"effort,omitempty"`
	HookEventName      HookEventName          `json:"hook_event_name"`
	Trigger            string                 `json:"trigger"`
	CustomInstructions requiredString         `json:"custom_instructions"`
	AgentID            optionalString         `json:"agent_id,omitempty"`
	AgentType          optionalString         `json:"agent_type,omitempty"`
}

type postCompactInput struct {
	SessionID      string                 `json:"session_id"`
	PromptID       optionalString         `json:"prompt_id,omitempty"`
	TranscriptPath requiredString         `json:"transcript_path"`
	CWD            string                 `json:"cwd"`
	PermissionMode optionalPermissionMode `json:"permission_mode,omitempty"`
	Effort         optionalEffort         `json:"effort,omitempty"`
	HookEventName  HookEventName          `json:"hook_event_name"`
	Trigger        string                 `json:"trigger"`
	CompactSummary requiredString         `json:"compact_summary"`
	AgentID        optionalString         `json:"agent_id,omitempty"`
	AgentType      optionalString         `json:"agent_type,omitempty"`
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

type optionalPermissionMode struct {
	value   string
	present bool
}

func (value *optionalPermissionMode) UnmarshalJSON(data []byte) error {
	value.present = true
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("must be a string")
	}
	return json.Unmarshal(data, &value.value)
}

type optionalEffort struct {
	Level   string `json:"level"`
	present bool
}

func (value *optionalEffort) UnmarshalJSON(data []byte) error {
	value.present = true
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("must be an object")
	}
	var decoded struct {
		Level string `json:"level"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	value.Level = decoded.Level
	return nil
}

type hookOutput struct {
	Continue           bool                `json:"continue"`
	Decision           string              `json:"decision,omitempty"`
	Reason             string              `json:"reason,omitempty"`
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
	if err := validateCommon(
		input.SessionID,
		input.CWD,
		input.HookEventName,
		EventSessionStart,
		input.TranscriptPath,
		input.PermissionMode.value,
		input.PermissionMode.present,
		input.Effort,
	); err != nil {
		return DecodedHook{}, err
	}
	if !oneOf(input.Source, "startup", "resume", "clear", "compact") {
		return DecodedHook{}, fmt.Errorf("unsupported source %q", input.Source)
	}

	metadata := commonMetadata(
		input.HookEventName,
		string(input.PromptID),
		input.PermissionMode.value,
		input.Effort.Level,
		string(input.AgentID),
		string(input.AgentType),
	)
	metadata["source"] = input.Source
	if input.Model != "" {
		metadata["model"] = string(input.Model)
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
	if err := validateCommon(
		input.SessionID,
		input.CWD,
		input.HookEventName,
		EventUserPromptSubmit,
		input.TranscriptPath,
		input.PermissionMode.value,
		input.PermissionMode.present,
		input.Effort,
	); err != nil {
		return DecodedHook{}, err
	}
	if !input.PermissionMode.present {
		return DecodedHook{}, fmt.Errorf("permission_mode is required")
	}
	if !input.Prompt.present {
		return DecodedHook{}, fmt.Errorf("prompt is required")
	}

	metadata := commonMetadata(
		input.HookEventName,
		string(input.PromptID),
		input.PermissionMode.value,
		input.Effort.Level,
		string(input.AgentID),
		string(input.AgentType),
	)
	event := newTransientEvent(
		input.SessionID,
		input.CWD,
		protocol.EventUserPrompt,
		occurredAt,
		metadata,
		eventIdentity(string(input.PromptID), occurredAt),
		string(input.AgentID),
	)
	event.Content = input.Prompt.value
	return DecodedHook{Name: input.HookEventName, Event: event}, nil
}

func decodeSubagentStart(raw []byte, occurredAt time.Time) (DecodedHook, error) {
	var input subagentStartInput
	if err := decodeStrict(raw, &input); err != nil {
		return DecodedHook{}, err
	}
	if err := validateCommon(
		input.SessionID,
		input.CWD,
		input.HookEventName,
		EventSubagentStart,
		input.TranscriptPath,
		input.PermissionMode.value,
		input.PermissionMode.present,
		input.Effort,
	); err != nil {
		return DecodedHook{}, err
	}
	if !input.AgentID.present || strings.TrimSpace(input.AgentID.value) == "" {
		return DecodedHook{}, fmt.Errorf("agent_id is required")
	}
	if !input.AgentType.present || strings.TrimSpace(input.AgentType.value) == "" {
		return DecodedHook{}, fmt.Errorf("agent_type is required")
	}

	metadata := commonMetadata(
		input.HookEventName,
		string(input.PromptID),
		input.PermissionMode.value,
		input.Effort.Level,
		input.AgentID.value,
		input.AgentType.value,
	)
	return DecodedHook{
		Name: input.HookEventName,
		Event: newTransientEvent(
			input.SessionID,
			input.CWD,
			protocol.EventSubagentStart,
			occurredAt,
			metadata,
			input.AgentID.value,
			input.AgentType.value,
		),
	}, nil
}

func decodeCompact(
	raw []byte,
	occurredAt time.Time,
	want HookEventName,
) (DecodedHook, error) {
	if want == EventPreCompact {
		var input preCompactInput
		if err := decodeStrict(raw, &input); err != nil {
			return DecodedHook{}, err
		}
		return normalizePreCompact(input, occurredAt)
	}

	var input postCompactInput
	if err := decodeStrict(raw, &input); err != nil {
		return DecodedHook{}, err
	}
	return normalizePostCompact(input, occurredAt)
}

func normalizePreCompact(input preCompactInput, occurredAt time.Time) (DecodedHook, error) {
	if err := validateCompactCommon(
		input.SessionID,
		input.CWD,
		input.HookEventName,
		EventPreCompact,
		input.TranscriptPath,
		input.PermissionMode.value,
		input.PermissionMode.present,
		input.Effort,
		input.Trigger,
	); err != nil {
		return DecodedHook{}, err
	}
	if !input.CustomInstructions.present {
		return DecodedHook{}, fmt.Errorf("custom_instructions is required")
	}

	metadata := commonMetadata(
		input.HookEventName,
		string(input.PromptID),
		input.PermissionMode.value,
		input.Effort.Level,
		string(input.AgentID),
		string(input.AgentType),
	)
	metadata["trigger"] = input.Trigger
	event := newTransientEvent(
		input.SessionID,
		input.CWD,
		protocol.EventPreCompact,
		occurredAt,
		metadata,
		input.Trigger,
		eventIdentity(string(input.PromptID), occurredAt),
		string(input.AgentID),
	)
	event.Content = input.CustomInstructions.value
	return DecodedHook{Name: input.HookEventName, Event: event}, nil
}

func normalizePostCompact(input postCompactInput, occurredAt time.Time) (DecodedHook, error) {
	if err := validateCompactCommon(
		input.SessionID,
		input.CWD,
		input.HookEventName,
		EventPostCompact,
		input.TranscriptPath,
		input.PermissionMode.value,
		input.PermissionMode.present,
		input.Effort,
		input.Trigger,
	); err != nil {
		return DecodedHook{}, err
	}
	if !input.CompactSummary.present {
		return DecodedHook{}, fmt.Errorf("compact_summary is required")
	}

	metadata := commonMetadata(
		input.HookEventName,
		string(input.PromptID),
		input.PermissionMode.value,
		input.Effort.Level,
		string(input.AgentID),
		string(input.AgentType),
	)
	metadata["trigger"] = input.Trigger
	event := newTransientEvent(
		input.SessionID,
		input.CWD,
		protocol.EventPostCompact,
		occurredAt,
		metadata,
		input.Trigger,
		eventIdentity(string(input.PromptID), occurredAt),
		string(input.AgentID),
	)
	event.Content = input.CompactSummary.value
	return DecodedHook{Name: input.HookEventName, Event: event}, nil
}

func validateCommon(
	sessionID string,
	cwd string,
	got HookEventName,
	want HookEventName,
	transcriptPath requiredString,
	permissionMode string,
	permissionModePresent bool,
	effort optionalEffort,
) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(cwd) == "" {
		return fmt.Errorf("cwd is required")
	}
	if got != want {
		return fmt.Errorf("hook_event_name must equal %q", want)
	}
	if !transcriptPath.present || strings.TrimSpace(transcriptPath.value) == "" {
		return fmt.Errorf("transcript_path is required")
	}
	if permissionModePresent && !oneOf(
		permissionMode,
		"default",
		"plan",
		"acceptEdits",
		"auto",
		"dontAsk",
		"bypassPermissions",
	) {
		return fmt.Errorf("unsupported permission_mode %q", permissionMode)
	}
	if effort.present && !oneOf(effort.Level, "low", "medium", "high", "xhigh", "max") {
		return fmt.Errorf("unsupported effort level %q", effort.Level)
	}
	return nil
}

func validateCompactCommon(
	sessionID string,
	cwd string,
	got HookEventName,
	want HookEventName,
	transcriptPath requiredString,
	permissionMode string,
	permissionModePresent bool,
	effort optionalEffort,
	trigger string,
) error {
	if err := validateCommon(
		sessionID,
		cwd,
		got,
		want,
		transcriptPath,
		permissionMode,
		permissionModePresent,
		effort,
	); err != nil {
		return err
	}
	if !oneOf(trigger, "manual", "auto") {
		return fmt.Errorf("unsupported trigger %q", trigger)
	}
	return nil
}

func validateOutput(eventName HookEventName, output Output) error {
	switch eventName {
	case EventSessionStart, EventUserPromptSubmit, EventSubagentStart:
	case EventPreCompact, EventPostCompact:
		if output.AdditionalContext != "" {
			return fmt.Errorf("Claude %s hook does not support additional context", eventName)
		}
	default:
		return fmt.Errorf("unsupported Claude hook event %q", eventName)
	}

	if output.Block {
		if eventName != EventUserPromptSubmit && eventName != EventPreCompact {
			return fmt.Errorf("Claude %s hook does not support blocking", eventName)
		}
		if strings.TrimSpace(output.BlockReason) == "" {
			return fmt.Errorf("block reason is required")
		}
	} else if output.BlockReason != "" {
		return fmt.Errorf("block reason requires block=true")
	}
	return nil
}

func commonMetadata(
	eventName HookEventName,
	promptID string,
	permissionMode string,
	effortLevel string,
	agentID string,
	agentType string,
) map[string]string {
	metadata := map[string]string{
		"host":            HostID,
		"hook_event_name": string(eventName),
	}
	if promptID != "" {
		metadata["prompt_id"] = promptID
	}
	if permissionMode != "" {
		metadata["permission_mode"] = permissionMode
	}
	if effortLevel != "" {
		metadata["effort_level"] = effortLevel
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
		ID:         "claude-" + hex.EncodeToString(digest[:]),
		SessionID:  sessionID,
		Kind:       kind,
		OccurredAt: occurredAt,
		CWD:        cwd,
		Metadata:   metadata,
	}
}

func eventIdentity(promptID string, occurredAt time.Time) string {
	if promptID != "" {
		return "prompt:" + promptID
	}
	return "received:" + occurredAt.UTC().Format(time.RFC3339Nano)
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
