package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

type CapsuleRecord struct {
	Category Category              `json:"category"`
	Record   protocol.MemoryRecord `json:"record"`
}

type CapsuleMetadata struct {
	SourceEventSeq        int64
	SourceOperationSeq    int64
	SourceViewDigest      string
	CompilerPolicyVersion string
	TokenCounterIdentity  string
	CreatedAt             time.Time
	RequiredLookupIDs     []string
}

// VerifiedCapsule is immutable derived context tied to an exact source view.
// ContentDigest covers both records and publication metadata.
type VerifiedCapsule struct {
	Records               []CapsuleRecord
	SourceEventSeq        int64
	SourceOperationSeq    int64
	SourceViewDigest      string
	CompilerPolicyVersion string
	TokenCounterIdentity  string
	CreatedAt             time.Time
	ContentDigest         string
	RequiredLookupIDs     []string
}

// PendingContext keeps the last verified capsule separate from newer durable
// operations. It is a foreground fallback, not a newly published capsule.
type PendingContext struct {
	Capsule             VerifiedCapsule
	Operations          []reducer.OperationEnvelope
	ThroughEventSeq     int64
	ThroughOperationSeq int64
}

func SealVerifiedCapsule(
	records []CapsuleRecord,
	metadata CapsuleMetadata,
) (VerifiedCapsule, error) {
	if err := validateCapsuleMetadata(metadata); err != nil {
		return VerifiedCapsule{}, err
	}
	capsule := VerifiedCapsule{
		Records:               cloneCapsuleRecords(records),
		SourceEventSeq:        metadata.SourceEventSeq,
		SourceOperationSeq:    metadata.SourceOperationSeq,
		SourceViewDigest:      metadata.SourceViewDigest,
		CompilerPolicyVersion: metadata.CompilerPolicyVersion,
		TokenCounterIdentity:  metadata.TokenCounterIdentity,
		CreatedAt:             metadata.CreatedAt,
		RequiredLookupIDs:     cloneStrings(metadata.RequiredLookupIDs),
	}
	digest, err := calculateCapsuleDigest(capsule)
	if err != nil {
		return VerifiedCapsule{}, err
	}
	capsule.ContentDigest = digest
	return capsule, nil
}

func ComposePendingContext(
	capsule VerifiedCapsule,
	operations []reducer.OperationEnvelope,
) (PendingContext, error) {
	if err := verifyCapsule(capsule); err != nil {
		return PendingContext{}, err
	}

	result := PendingContext{
		Capsule:             cloneVerifiedCapsule(capsule),
		Operations:          make([]reducer.OperationEnvelope, 0, len(operations)),
		ThroughEventSeq:     capsule.SourceEventSeq,
		ThroughOperationSeq: capsule.SourceOperationSeq,
	}
	expectedOperationSeq := capsule.SourceOperationSeq + 1
	lastEventSeq := capsule.SourceEventSeq
	operationIDs := make(map[string]struct{}, len(operations))
	for index, envelope := range operations {
		if envelope.OperationSeq != expectedOperationSeq {
			return PendingContext{}, fmt.Errorf(
				"operations[%d] sequence is %d, want %d after verified capsule",
				index,
				envelope.OperationSeq,
				expectedOperationSeq,
			)
		}
		if envelope.EventSeq <= capsule.SourceEventSeq {
			return PendingContext{}, fmt.Errorf(
				"operations[%d] event sequence %d is not newer than capsule event sequence %d",
				index,
				envelope.EventSeq,
				capsule.SourceEventSeq,
			)
		}
		if envelope.EventSeq < lastEventSeq {
			return PendingContext{}, fmt.Errorf(
				"operations[%d] event sequence %d is older than previous event sequence %d",
				index,
				envelope.EventSeq,
				lastEventSeq,
			)
		}
		if err := validatePendingOperation(envelope); err != nil {
			return PendingContext{}, fmt.Errorf("operations[%d]: %w", index, err)
		}
		if _, duplicate := operationIDs[envelope.Operation.ID]; duplicate {
			return PendingContext{}, fmt.Errorf(
				"operations[%d] duplicates operation id %q",
				index,
				envelope.Operation.ID,
			)
		}
		operationIDs[envelope.Operation.ID] = struct{}{}

		result.Operations = append(result.Operations, cloneOperationEnvelope(envelope))
		result.ThroughEventSeq = envelope.EventSeq
		result.ThroughOperationSeq = envelope.OperationSeq
		expectedOperationSeq++
		lastEventSeq = envelope.EventSeq
	}
	return result, nil
}

func validateCapsuleMetadata(metadata CapsuleMetadata) error {
	if metadata.SourceEventSeq < 0 {
		return fmt.Errorf("source event sequence must not be negative")
	}
	if metadata.SourceOperationSeq < 0 {
		return fmt.Errorf("source operation sequence must not be negative")
	}
	if !validSHA256(metadata.SourceViewDigest) {
		return fmt.Errorf("source view digest must be a SHA-256 hex digest")
	}
	if strings.TrimSpace(metadata.CompilerPolicyVersion) == "" {
		return fmt.Errorf("compiler policy version is required")
	}
	if strings.TrimSpace(metadata.TokenCounterIdentity) == "" {
		return fmt.Errorf("token counter identity is required")
	}
	if metadata.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	zoneName, zoneOffset := metadata.CreatedAt.Zone()
	if zoneOffset != 0 {
		return fmt.Errorf("created_at must use UTC, got zone %q", zoneName)
	}
	seenLookupIDs := make(map[string]struct{}, len(metadata.RequiredLookupIDs))
	for index, id := range metadata.RequiredLookupIDs {
		if !capsuleLookupIDPattern.MatchString(id) {
			return fmt.Errorf("required_lookup_ids[%d] is not a valid record id", index)
		}
		if _, exists := seenLookupIDs[id]; exists {
			return fmt.Errorf("required_lookup_ids[%d] duplicates %q", index, id)
		}
		seenLookupIDs[id] = struct{}{}
	}
	return nil
}

func verifyCapsule(capsule VerifiedCapsule) error {
	metadata := CapsuleMetadata{
		SourceEventSeq:        capsule.SourceEventSeq,
		SourceOperationSeq:    capsule.SourceOperationSeq,
		SourceViewDigest:      capsule.SourceViewDigest,
		CompilerPolicyVersion: capsule.CompilerPolicyVersion,
		TokenCounterIdentity:  capsule.TokenCounterIdentity,
		CreatedAt:             capsule.CreatedAt,
		RequiredLookupIDs:     capsule.RequiredLookupIDs,
	}
	if err := validateCapsuleMetadata(metadata); err != nil {
		return fmt.Errorf("verify capsule metadata: %w", err)
	}
	if !validSHA256(capsule.ContentDigest) {
		return fmt.Errorf("verify capsule: content digest must be a SHA-256 hex digest")
	}
	digest, err := calculateCapsuleDigest(capsule)
	if err != nil {
		return err
	}
	if digest != capsule.ContentDigest {
		return fmt.Errorf(
			"verify capsule: content digest mismatch: stored %s, calculated %s",
			capsule.ContentDigest,
			digest,
		)
	}
	return nil
}

func calculateCapsuleDigest(capsule VerifiedCapsule) (string, error) {
	payload := struct {
		Records               []CapsuleRecord `json:"records"`
		SourceEventSeq        int64           `json:"source_event_seq"`
		SourceOperationSeq    int64           `json:"source_operation_seq"`
		SourceViewDigest      string          `json:"source_view_digest"`
		CompilerPolicyVersion string          `json:"compiler_policy_version"`
		TokenCounterIdentity  string          `json:"token_counter_identity"`
		CreatedAt             time.Time       `json:"created_at"`
		RequiredLookupIDs     []string        `json:"required_lookup_ids,omitempty"`
	}{
		Records:               capsule.Records,
		SourceEventSeq:        capsule.SourceEventSeq,
		SourceOperationSeq:    capsule.SourceOperationSeq,
		SourceViewDigest:      capsule.SourceViewDigest,
		CompilerPolicyVersion: capsule.CompilerPolicyVersion,
		TokenCounterIdentity:  capsule.TokenCounterIdentity,
		CreatedAt:             capsule.CreatedAt,
		RequiredLookupIDs:     capsule.RequiredLookupIDs,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode capsule digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validatePendingOperation(envelope reducer.OperationEnvelope) error {
	if envelope.OperationSeq <= 0 {
		return fmt.Errorf("operation sequence must be positive")
	}
	if envelope.EventSeq <= 0 {
		return fmt.Errorf("event sequence must be positive")
	}
	batch := protocol.MutationBatch{
		Protocol:      protocol.Version,
		PrivacyMode:   envelope.PrivacyMode,
		SourceEventID: envelope.SourceEventID,
		CreatedAt:     envelope.CreatedAt,
		Operations:    []protocol.Operation{envelope.Operation},
	}
	if err := protocol.ValidateMutationBatch(batch); err != nil {
		return fmt.Errorf("validate durable operation: %w", err)
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func cloneVerifiedCapsule(capsule VerifiedCapsule) VerifiedCapsule {
	capsule.Records = cloneCapsuleRecords(capsule.Records)
	capsule.RequiredLookupIDs = cloneStrings(capsule.RequiredLookupIDs)
	return capsule
}

func cloneCapsuleRecords(records []CapsuleRecord) []CapsuleRecord {
	cloned := make([]CapsuleRecord, len(records))
	for index, record := range records {
		cloned[index] = record
		cloned[index].Record = cloneProtocolRecord(record.Record)
	}
	return cloned
}

func cloneOperationEnvelope(envelope reducer.OperationEnvelope) reducer.OperationEnvelope {
	if envelope.Operation.Record != nil {
		record := cloneProtocolRecord(*envelope.Operation.Record)
		envelope.Operation.Record = &record
	}
	return envelope
}

func cloneProtocolRecord(record protocol.MemoryRecord) protocol.MemoryRecord {
	if record.ExpiresAt != nil {
		expiresAt := *record.ExpiresAt
		record.ExpiresAt = &expiresAt
	}
	return record
}

var capsuleLookupIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}
