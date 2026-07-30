// Package protocol defines the stable data exchanged between agent adapters,
// candidate extractors, and the deterministic context-compactor core.
package protocol

import "time"

const Version = "context-compactor/v1"

type PrivacyMode string

const (
	// PrivacyStandard is the only production policy. Its wire value stays
	// "balanced" for context-compactor/v1 data compatibility.
	PrivacyStandard PrivacyMode = "balanced"

	PrivacyStrict   PrivacyMode = "strict"
	PrivacyBalanced PrivacyMode = PrivacyStandard
	PrivacyAudit    PrivacyMode = "audit"
)

type EventKind string

const (
	EventSessionStart    EventKind = "session_start"
	EventSubagentStart   EventKind = "subagent_start"
	EventUserPrompt      EventKind = "user_prompt"
	EventAssistantResult EventKind = "assistant_result"
	EventToolResult      EventKind = "tool_result"
	EventCheckpoint      EventKind = "checkpoint"
	EventPreCompact      EventKind = "pre_compact"
	EventPostCompact     EventKind = "post_compact"
)

// TransientEvent may contain prompt or tool content while it moves through the
// extraction pipeline. It is not a durable journal record.
type TransientEvent struct {
	Protocol   string            `json:"protocol"`
	ID         string            `json:"id"`
	SessionID  string            `json:"session_id"`
	Kind       EventKind         `json:"kind"`
	OccurredAt time.Time         `json:"occurred_at"`
	CWD        string            `json:"cwd"`
	Content    string            `json:"content,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// IncomingEvent is the plain-English name used by the active runtime flow.
// TransientEvent remains the version 1 compatibility name.
type IncomingEvent = TransientEvent

type OperationKind string

const (
	OperationAdd       OperationKind = "add"
	OperationSupersede OperationKind = "supersede"
	OperationResolve   OperationKind = "resolve"
	OperationExpire    OperationKind = "expire"
)

type MemoryKind string

const (
	MemoryGoal                MemoryKind = "goal"
	MemoryAcceptanceCriterion MemoryKind = "acceptance_criterion"
	MemoryConstraint          MemoryKind = "constraint"
	MemoryDecision            MemoryKind = "decision"
	MemoryBlocker             MemoryKind = "blocker"
	MemoryQuestion            MemoryKind = "question"
	MemoryTask                MemoryKind = "task"
	MemoryFile                MemoryKind = "file"
	MemoryTestResult          MemoryKind = "test_result"
)

type Priority string

const (
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityNormal   Priority = "normal"
	PriorityLow      Priority = "low"
)

type Confidence string

const (
	ConfidenceExplicit Confidence = "explicit"
	ConfidenceVerified Confidence = "verified"
	ConfidenceInferred Confidence = "inferred"
)

type RecordStatus string

const (
	StatusActive RecordStatus = "active"
)

type ExtractionOutcome string

const (
	OutcomeNoChange     ExtractionOutcome = "no_change"
	OutcomeMemoryUpdate ExtractionOutcome = "memory_update"
)

// SourceReference keeps durable memory traceable without requiring a complete
// prompt. Evidence is privacy-mode dependent and Artifact points to a
// repository or local file that can be inspected when reconciliation is needed.
type SourceReference struct {
	EventID  string `json:"event_id"`
	Evidence string `json:"evidence,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

type MemorySource = SourceReference

type MemoryRecord struct {
	ID          string          `json:"id"`
	ConflictKey string          `json:"conflict_key,omitempty"`
	Kind        MemoryKind      `json:"kind"`
	Value       string          `json:"value"`
	Priority    Priority        `json:"priority"`
	Confidence  Confidence      `json:"confidence"`
	Status      RecordStatus    `json:"status"`
	Source      SourceReference `json:"source"`
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"`
}

type MemoryItem = MemoryRecord

type Operation struct {
	ID       string        `json:"id"`
	Kind     OperationKind `json:"kind"`
	TargetID string        `json:"target_id,omitempty"`
	Record   *MemoryRecord `json:"record,omitempty"`
}

type MemoryChange = Operation

// MemoryUpdate is durable candidate output. It intentionally has no field for
// complete prompt content, and strict decoding rejects unknown fields.
type MemoryUpdate struct {
	Protocol      string      `json:"protocol"`
	PrivacyMode   PrivacyMode `json:"privacy_mode"`
	SourceEventID string      `json:"source_event_id"`
	CreatedAt     time.Time   `json:"created_at"`
	Operations    []Operation `json:"operations"`
}

// MutationBatch preserves the version 1 Go API while new code uses the clearer
// MemoryUpdate name. Both names have the same JSON representation.
type MutationBatch = MemoryUpdate

// ExtractionResult is the only document a model-assisted extractor may
// return. A no-change result has no update; a memory-update result has exactly
// one protocol-valid update.
type ExtractionResult struct {
	Protocol     string            `json:"protocol"`
	Outcome      ExtractionOutcome `json:"outcome"`
	MemoryUpdate *MemoryUpdate     `json:"memory_update,omitempty"`
}
