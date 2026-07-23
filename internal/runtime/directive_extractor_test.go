package runtime

import (
	"context"
	"strings"
	"testing"

	"context-compactor/internal/protocol"
)

func TestDirectiveExtractorPersistsOnlyExplicitBoundedDirectives(t *testing.T) {
	event := validJournalHandlerEvent()
	event.Content = strings.Join([]string{
		"ordinary prompt text must remain transient",
		"[context-compactor] goal: Ship the executable hook runtime.",
		"[context-compactor] decision: Use a durable SQLite refresh queue.",
		"[context-compactor] unknown: ignored",
	}, "\n")

	extraction, err := (DirectiveExtractor{}).Extract(
		context.Background(),
		event,
		protocol.PrivacyBalanced,
	)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if extraction.Batch == nil || len(extraction.Batch.Operations) != 2 {
		t.Fatalf("Extract() batch = %+v, want two operations", extraction.Batch)
	}
	for _, operation := range extraction.Batch.Operations {
		if operation.Record == nil {
			t.Fatalf("operation = %+v, want record", operation)
		}
		if strings.Contains(operation.Record.Value, "ordinary prompt") {
			t.Fatal("ordinary prompt text was persisted as memory")
		}
		if operation.Record.Source.Evidence != "" {
			t.Fatal("directive extractor persisted evidence text")
		}
		if operation.Record.Confidence != protocol.ConfidenceExplicit {
			t.Fatalf("record confidence = %q, want explicit", operation.Record.Confidence)
		}
	}
}

func TestDirectiveExtractorFiltersSecretCandidatesAndSupportsLifecycle(t *testing.T) {
	event := validJournalHandlerEvent()
	event.Content = strings.Join([]string{
		"[context-compactor] task: token=not-a-real-credential",
		"[context-compactor] resolve: record-existing",
		"[context-compactor] expire: record-old",
	}, "\n")

	extraction, err := (DirectiveExtractor{}).Extract(
		context.Background(),
		event,
		protocol.PrivacyStrict,
	)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if extraction.RedactionCount != 1 {
		t.Fatalf("RedactionCount = %d, want 1", extraction.RedactionCount)
	}
	if extraction.Batch == nil || len(extraction.Batch.Operations) != 2 {
		t.Fatalf("Extract() batch = %+v, want lifecycle operations", extraction.Batch)
	}
	if extraction.Batch.Operations[0].Kind != protocol.OperationResolve ||
		extraction.Batch.Operations[1].Kind != protocol.OperationExpire {
		t.Fatalf("lifecycle operations = %+v", extraction.Batch.Operations)
	}
}

func TestDirectiveExtractorIsIdempotentForStableEvent(t *testing.T) {
	event := validJournalHandlerEvent()
	event.Content = "[context-compactor] task: Complete runtime integration."
	extractor := DirectiveExtractor{}

	first, err := extractor.Extract(context.Background(), event, protocol.PrivacyBalanced)
	if err != nil {
		t.Fatalf("first Extract() error = %v", err)
	}
	second, err := extractor.Extract(context.Background(), event, protocol.PrivacyBalanced)
	if err != nil {
		t.Fatalf("second Extract() error = %v", err)
	}
	if first.Batch.Operations[0].ID != second.Batch.Operations[0].ID ||
		first.Batch.Operations[0].Record.ID != second.Batch.Operations[0].Record.ID {
		t.Fatalf("stable extraction ids differ: first=%+v second=%+v", first, second)
	}
}
