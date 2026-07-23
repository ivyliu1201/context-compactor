package compiler

import (
	"fmt"
	"time"

	"context-compactor/internal/reducer"
)

const CompilerPolicyVersion = "context-compactor/compiler/v1"

// BuildVerifiedCapsule converts one already budgeted context into an immutable
// derived capsule tied to an exact materialized-view snapshot.
func BuildVerifiedCapsule(
	compiled CompiledContext,
	sourceEventSeq int64,
	view reducer.View,
	createdAt time.Time,
) (VerifiedCapsule, error) {
	if _, err := RenderCompiledContext(compiled); err != nil {
		return VerifiedCapsule{}, fmt.Errorf("validate compiled capsule: %w", err)
	}
	if sourceEventSeq < 0 {
		return VerifiedCapsule{}, fmt.Errorf("source event sequence must not be negative")
	}
	if view.LastOperationSeq < 0 {
		return VerifiedCapsule{}, fmt.Errorf("source operation sequence must not be negative")
	}

	records := make([]CapsuleRecord, 0, len(compiled.Records))
	requiredLookupIDs := []string(nil)
	if compiled.Recovery != nil {
		if compiled.Recovery.SourceOperationSeq != view.LastOperationSeq ||
			compiled.Recovery.SourceViewDigest != view.Digest {
			return VerifiedCapsule{}, fmt.Errorf(
				"recovery capsule source does not match materialized view",
			)
		}
		records = make([]CapsuleRecord, 0, len(compiled.Recovery.Records))
		for _, record := range compiled.Recovery.Records {
			records = append(records, CapsuleRecord{
				Category: record.Category,
				Record:   record.Record,
			})
		}
		requiredLookupIDs = cloneStrings(compiled.Recovery.RequiredLookupIDs)
	} else {
		for _, record := range compiled.Records {
			records = append(records, CapsuleRecord{
				Category: record.Category,
				Record:   record.Record.Record,
			})
		}
	}

	return SealVerifiedCapsule(records, CapsuleMetadata{
		SourceEventSeq:        sourceEventSeq,
		SourceOperationSeq:    view.LastOperationSeq,
		SourceViewDigest:      view.Digest,
		CompilerPolicyVersion: CompilerPolicyVersion,
		TokenCounterIdentity:  compiled.CounterIdentity,
		CreatedAt:             createdAt,
		RequiredLookupIDs:     requiredLookupIDs,
	})
}
