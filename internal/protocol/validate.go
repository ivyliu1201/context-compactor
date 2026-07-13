package protocol

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxIDRunes               = 128
	maxCWDrunes              = 4096
	maxTransientContentRunes = 256_000
	maxMetadataEntries       = 32
	maxMetadataKeyRunes      = 64
	maxMetadataValueRunes    = 1024
	maxOperations            = 100
	maxMemoryValueRunes      = 2000
	maxArtifactRunes         = 1024
	maxBalancedEvidenceRunes = 280
	maxAuditEvidenceRunes    = 8000
)

var (
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	secretPattern = regexp.MustCompile(
		`(?i)(authorization\s*:\s*(bearer|basic)\s+\S+|` +
			`(?:api[_-]?key|token|secret|password|passwd|pwd)\s*[:=]\s*["']?\S+|` +
			`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|` +
			`\bsk-[A-Za-z0-9_-]{8,}\b)`,
	)
)

func ValidateTransientEvent(event TransientEvent) error {
	if event.Protocol != Version {
		return fmt.Errorf("protocol must equal %q", Version)
	}
	if err := validateID("event id", event.ID); err != nil {
		return err
	}
	if err := validateID("session id", event.SessionID); err != nil {
		return err
	}
	if !validEventKind(event.Kind) {
		return fmt.Errorf("unsupported event kind %q", event.Kind)
	}
	if err := validateUTCTime("occurred_at", event.OccurredAt); err != nil {
		return err
	}
	if strings.TrimSpace(event.CWD) == "" {
		return fmt.Errorf("cwd is required")
	}
	if utf8.RuneCountInString(event.CWD) > maxCWDrunes {
		return fmt.Errorf("cwd exceeds %d characters", maxCWDrunes)
	}
	if utf8.RuneCountInString(event.Content) > maxTransientContentRunes {
		return fmt.Errorf("transient content exceeds %d characters", maxTransientContentRunes)
	}
	if len(event.Metadata) > maxMetadataEntries {
		return fmt.Errorf("metadata exceeds %d entries", maxMetadataEntries)
	}
	for key, value := range event.Metadata {
		if strings.TrimSpace(key) == "" || utf8.RuneCountInString(key) > maxMetadataKeyRunes {
			return fmt.Errorf("metadata key must contain 1-%d characters", maxMetadataKeyRunes)
		}
		if utf8.RuneCountInString(value) > maxMetadataValueRunes {
			return fmt.Errorf("metadata value for %q exceeds %d characters", key, maxMetadataValueRunes)
		}
	}
	return nil
}

func ValidateMutationBatch(batch MutationBatch) error {
	if batch.Protocol != Version {
		return fmt.Errorf("protocol must equal %q", Version)
	}
	if !validPrivacyMode(batch.PrivacyMode) {
		return fmt.Errorf("unsupported privacy mode %q", batch.PrivacyMode)
	}
	if err := validateID("source event id", batch.SourceEventID); err != nil {
		return err
	}
	if err := validateUTCTime("created_at", batch.CreatedAt); err != nil {
		return err
	}
	if len(batch.Operations) == 0 {
		return fmt.Errorf("operations must not be empty")
	}
	if len(batch.Operations) > maxOperations {
		return fmt.Errorf("operations exceeds %d entries", maxOperations)
	}

	operationIDs := make(map[string]struct{}, len(batch.Operations))
	recordIDs := make(map[string]struct{}, len(batch.Operations))
	for index, operation := range batch.Operations {
		if err := validateOperation(operation, batch, recordIDs); err != nil {
			return fmt.Errorf("operations[%d]: %w", index, err)
		}
		if _, exists := operationIDs[operation.ID]; exists {
			return fmt.Errorf("operations[%d]: duplicate operation id %q", index, operation.ID)
		}
		operationIDs[operation.ID] = struct{}{}
	}
	return nil
}

func validateOperation(operation Operation, batch MutationBatch, recordIDs map[string]struct{}) error {
	if err := validateID("operation id", operation.ID); err != nil {
		return err
	}
	if !validOperationKind(operation.Kind) {
		return fmt.Errorf("unsupported operation kind %q", operation.Kind)
	}

	switch operation.Kind {
	case OperationAdd:
		if operation.TargetID != "" {
			return fmt.Errorf("add operation must not include target_id")
		}
		if operation.Record == nil {
			return fmt.Errorf("add operation requires record")
		}
	case OperationSupersede:
		if err := validateID("target id", operation.TargetID); err != nil {
			return err
		}
		if operation.Record == nil {
			return fmt.Errorf("supersede operation requires replacement record")
		}
		if operation.Record.ID == operation.TargetID {
			return fmt.Errorf("replacement record id must differ from target_id")
		}
	case OperationResolve, OperationExpire:
		if err := validateID("target id", operation.TargetID); err != nil {
			return err
		}
		if operation.Record != nil {
			return fmt.Errorf("%s operation must not include record", operation.Kind)
		}
	}

	if operation.Record == nil {
		return nil
	}
	if err := validateMemoryRecord(*operation.Record, batch); err != nil {
		return err
	}
	if _, exists := recordIDs[operation.Record.ID]; exists {
		return fmt.Errorf("duplicate record id %q", operation.Record.ID)
	}
	recordIDs[operation.Record.ID] = struct{}{}
	return nil
}

func validateMemoryRecord(record MemoryRecord, batch MutationBatch) error {
	if err := validateID("record id", record.ID); err != nil {
		return err
	}
	if !validMemoryKind(record.Kind) {
		return fmt.Errorf("unsupported memory kind %q", record.Kind)
	}
	value := strings.TrimSpace(record.Value)
	if value == "" {
		return fmt.Errorf("record value is required")
	}
	if utf8.RuneCountInString(value) > maxMemoryValueRunes {
		return fmt.Errorf("record value exceeds %d characters", maxMemoryValueRunes)
	}
	if !validPriority(record.Priority) {
		return fmt.Errorf("unsupported priority %q", record.Priority)
	}
	if !validConfidence(record.Confidence) {
		return fmt.Errorf("unsupported confidence %q", record.Confidence)
	}
	if record.Priority == PriorityCritical && record.Confidence == ConfidenceInferred {
		return fmt.Errorf("critical record cannot use inferred confidence")
	}
	if record.Status != StatusActive {
		return fmt.Errorf("new or replacement record status must equal %q", StatusActive)
	}
	if record.Source.EventID != batch.SourceEventID {
		return fmt.Errorf("record source event_id must match batch source_event_id")
	}
	if err := validateEvidence(record, batch.PrivacyMode); err != nil {
		return err
	}
	if utf8.RuneCountInString(record.Source.Artifact) > maxArtifactRunes {
		return fmt.Errorf("source artifact exceeds %d characters", maxArtifactRunes)
	}
	if err := validateUTCTime("record created_at", record.CreatedAt); err != nil {
		return err
	}
	if record.CreatedAt.After(batch.CreatedAt) {
		return fmt.Errorf("record created_at must not be after batch created_at")
	}
	if record.ExpiresAt != nil {
		if err := validateUTCTime("record expires_at", *record.ExpiresAt); err != nil {
			return err
		}
		if !record.ExpiresAt.After(record.CreatedAt) {
			return fmt.Errorf("record expires_at must be after created_at")
		}
	}
	return nil
}

func validateEvidence(record MemoryRecord, mode PrivacyMode) error {
	evidence := strings.TrimSpace(record.Source.Evidence)
	if secretPattern.MatchString(evidence) {
		return fmt.Errorf("source evidence appears to contain a secret")
	}

	switch mode {
	case PrivacyStrict:
		if evidence != "" {
			return fmt.Errorf("strict privacy mode forbids evidence text")
		}
	case PrivacyBalanced:
		if utf8.RuneCountInString(evidence) > maxBalancedEvidenceRunes {
			return fmt.Errorf("balanced evidence exceeds %d characters", maxBalancedEvidenceRunes)
		}
		if record.Priority == PriorityCritical && evidence == "" && strings.TrimSpace(record.Source.Artifact) == "" {
			return fmt.Errorf("critical balanced record requires evidence or artifact")
		}
	case PrivacyAudit:
		if utf8.RuneCountInString(evidence) > maxAuditEvidenceRunes {
			return fmt.Errorf("audit evidence exceeds %d characters", maxAuditEvidenceRunes)
		}
	}
	return nil
}

func validateID(name, value string) error {
	if !idPattern.MatchString(value) || utf8.RuneCountInString(value) > maxIDRunes {
		return fmt.Errorf("%s must match %s", name, idPattern.String())
	}
	return nil
}

func validateUTCTime(name string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", name)
	}
	zoneName, zoneOffset := value.Zone()
	if zoneOffset != 0 {
		return fmt.Errorf("%s must use UTC, got zone %q", name, zoneName)
	}
	return nil
}

func validPrivacyMode(mode PrivacyMode) bool {
	return mode == PrivacyStrict || mode == PrivacyBalanced || mode == PrivacyAudit
}

func validEventKind(kind EventKind) bool {
	switch kind {
	case EventSessionStart, EventUserPrompt, EventAssistantResult, EventToolResult,
		EventCheckpoint, EventPreCompact, EventPostCompact:
		return true
	default:
		return false
	}
}

func validOperationKind(kind OperationKind) bool {
	return kind == OperationAdd || kind == OperationSupersede ||
		kind == OperationResolve || kind == OperationExpire
}

func validMemoryKind(kind MemoryKind) bool {
	switch kind {
	case MemoryGoal, MemoryAcceptanceCriterion, MemoryConstraint, MemoryDecision,
		MemoryBlocker, MemoryQuestion, MemoryTask, MemoryFile, MemoryTestResult:
		return true
	default:
		return false
	}
}

func validPriority(priority Priority) bool {
	return priority == PriorityCritical || priority == PriorityHigh ||
		priority == PriorityNormal || priority == PriorityLow
}

func validConfidence(confidence Confidence) bool {
	return confidence == ConfidenceExplicit || confidence == ConfidenceVerified ||
		confidence == ConfidenceInferred
}
