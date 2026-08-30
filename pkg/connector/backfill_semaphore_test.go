package connector

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type cancelAfterInitialCheckContext struct {
	context.Context
	errCalls int
}

func (c *cancelAfterInitialCheckContext) Err() error {
	c.errCalls++
	if c.errCalls == 1 {
		return nil
	}
	return context.Canceled
}

func TestWaitForForwardBackfillSlotCancellation(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForForwardBackfillSlot(ctx, sem)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForForwardBackfillSlot error = %v, want context.Canceled", err)
	}
	if len(sem) != 1 {
		t.Fatalf("semaphore occupancy = %d, want 1", len(sem))
	}
}

func TestWaitForForwardBackfillSlotAcquires(t *testing.T) {
	sem := make(chan struct{}, 1)
	if err := waitForForwardBackfillSlot(context.Background(), sem); err != nil {
		t.Fatalf("waitForForwardBackfillSlot: %v", err)
	}
	if len(sem) != 1 {
		t.Fatalf("semaphore occupancy = %d, want 1", len(sem))
	}
}

func TestWaitForForwardBackfillSlotAlreadyCancelledDoesNotAcquire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := 0; i < 100; i++ {
		sem := make(chan struct{}, 1)
		err := waitForForwardBackfillSlot(ctx, sem)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attempt %d: error = %v, want context.Canceled", i, err)
		}
		if len(sem) != 0 {
			t.Fatalf("attempt %d: cancelled call acquired semaphore", i)
		}
	}
}

func TestWaitForForwardBackfillSlotCancellationAfterAcquireReleasesSlot(t *testing.T) {
	ctx := &cancelAfterInitialCheckContext{Context: context.Background()}
	sem := make(chan struct{}, 1)
	err := waitForForwardBackfillSlot(ctx, sem)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForForwardBackfillSlot error = %v, want context.Canceled", err)
	}
	if len(sem) != 0 {
		t.Fatalf("semaphore occupancy = %d, want released slot", len(sem))
	}
}

func TestFetchMessagesForwardSemaphoreCancellationCompletesAccounting(t *testing.T) {
	_, store := newTestCloudBackfillStore(t)
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	client := &IMClient{
		Main: &IMConnector{
			Config: IMConfig{CloudKitBackfill: true},
		},
		cloudStore:         store,
		forwardBackfillSem: sem,
	}
	atomic.StoreInt64(&client.pendingInitialBackfills, 2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err := client.FetchMessages(ctx, bridgev2.FetchMessagesParams{
		Portal: &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{
			ID:       "test-portal",
			Receiver: testSQLLoginID,
		}}},
		Forward: true,
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchMessages error = %v, want context.Canceled", err)
	}
	if response != nil {
		t.Fatalf("FetchMessages response = %#v, want nil", response)
	}
	if got := atomic.LoadInt64(&client.pendingInitialBackfills); got != 1 {
		t.Fatalf("pendingInitialBackfills = %d, want 1", got)
	}
	if len(sem) != 1 {
		t.Fatalf("semaphore occupancy = %d, want original holder only", len(sem))
	}
}
