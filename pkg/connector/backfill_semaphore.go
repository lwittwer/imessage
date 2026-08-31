package connector

import "context"

// waitForForwardBackfillSlot acquires a forward-backfill slot or returns when
// the portal lifecycle ends.
func waitForForwardBackfillSlot(ctx context.Context, sem chan struct{}) error {
	// A select chooses randomly when both cases are ready. Check first so an
	// already-cancelled request never acquires a newly-available slot and then
	// proceeds.
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case sem <- struct{}{}:
		// Cancellation may race the send and make both select cases ready. If
		// the send won, hand the slot back before returning.
		if err := ctx.Err(); err != nil {
			<-sem
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
