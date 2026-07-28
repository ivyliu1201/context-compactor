package journal

import (
	"context"
	"strings"
	"testing"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

func TestAppendAndRebuildMemoryViewCommitsValidatedViewAtomically(t *testing.T) {
	ctx := context.Background()
	store, root := openTestStore(t)
	request := validAppendRequest(root, "event-atomic-1", "operation-atomic-1")

	appendResult, snapshot, err := store.AppendAndRebuildMemoryView(ctx, request)
	if err != nil {
		t.Fatalf("AppendAndRebuildMemoryView() error = %v", err)
	}
	if !appendResult.EventInserted || appendResult.OperationsInserted != 1 {
		t.Fatalf("append result = %+v", appendResult)
	}
	if snapshot.LastEventSeq != appendResult.EventSeq ||
		snapshot.View.LastOperationSeq != 1 ||
		len(snapshot.View.Records) != 1 {
		t.Fatalf("memory snapshot = %+v", snapshot)
	}
}

func TestAppendAndRebuildMemoryViewRollsBackInvalidLifecycleOperation(t *testing.T) {
	ctx := context.Background()
	store, root := openTestStore(t)
	request := validAppendRequest(root, "event-atomic-invalid", "operation-atomic-invalid")
	request.Batch.Operations[0] = protocol.Operation{
		ID:       "operation-atomic-invalid",
		Kind:     protocol.OperationResolve,
		TargetID: "record-missing",
	}

	if _, _, err := store.AppendAndRebuildMemoryView(ctx, request); err == nil ||
		!strings.Contains(err.Error(), "reduce appended memory operations") {
		t.Fatalf("AppendAndRebuildMemoryView() error = %v, want reducer failure", err)
	}
	var eventCount, operationCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM memory_operations").Scan(
		&operationCount,
	); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if eventCount != 0 || operationCount != 0 {
		t.Fatalf(
			"rolled back counts = events %d, operations %d",
			eventCount,
			operationCount,
		)
	}
}
