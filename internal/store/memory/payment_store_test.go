package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestPaymentStoreTracksProcessedEvents(t *testing.T) {
	ctx := context.Background()
	payments := NewPaymentStore()

	processed, err := payments.IsEventProcessed(ctx, "evt_1")
	if err != nil {
		t.Fatalf("is event processed: %v", err)
	}
	if processed {
		t.Error("evt_1 is processed before it was marked")
	}

	if err := payments.MarkEventProcessed(ctx, "evt_1"); err != nil {
		t.Fatalf("mark event processed: %v", err)
	}
	processed, err = payments.IsEventProcessed(ctx, "evt_1")
	if err != nil {
		t.Fatalf("is event processed: %v", err)
	}
	if !processed {
		t.Error("evt_1 is not processed after it was marked")
	}

	// Marking the same id again is a no-op that still succeeds, so a replayed
	// callback can be handled safely.
	if err := payments.MarkEventProcessed(ctx, "evt_1"); err != nil {
		t.Fatalf("mark evt_1 a second time: %v", err)
	}
	processed, err = payments.IsEventProcessed(ctx, "evt_1")
	if err != nil {
		t.Fatalf("is event processed: %v", err)
	}
	if !processed {
		t.Error("evt_1 is not processed after it was marked twice")
	}

	processed, err = payments.IsEventProcessed(ctx, "evt_2")
	if err != nil {
		t.Fatalf("is event processed: %v", err)
	}
	if processed {
		t.Error("evt_2 is processed, want an unseen id to be unprocessed")
	}
}

func TestPaymentStoreEventsAreSafeForConcurrentUse(t *testing.T) {
	ctx := context.Background()
	payments := NewPaymentStore()

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers * 2)
	for i := 0; i < workers; i++ {
		go func(worker int) {
			defer wg.Done()
			id := fmt.Sprintf("evt_%d", worker)
			if err := payments.MarkEventProcessed(ctx, id); err != nil {
				t.Errorf("mark %s: %v", id, err)
			}
		}(i)
		go func(worker int) {
			defer wg.Done()
			id := fmt.Sprintf("evt_%d", worker)
			if _, err := payments.IsEventProcessed(ctx, id); err != nil {
				t.Errorf("is processed %s: %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		id := fmt.Sprintf("evt_%d", i)
		processed, err := payments.IsEventProcessed(ctx, id)
		if err != nil {
			t.Fatalf("is processed %s: %v", id, err)
		}
		if !processed {
			t.Errorf("%s is not processed after it was marked", id)
		}
	}
}
