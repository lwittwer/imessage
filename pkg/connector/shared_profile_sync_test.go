package connector

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
	"github.com/rs/zerolog"
)

type testSharedProfileFetcher func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error)

func (f testSharedProfileFetcher) FetchProfile(recordKey string, decryptionKey []byte, hasPoster bool) (rustpushgo.WrappedProfileRecord, error) {
	return f(recordKey, decryptionKey, hasPoster)
}

func newLocalProfileSyncTestClient(t *testing.T) (*IMClient, *sharedProfileStore) {
	t.Helper()
	store := newSharedProfileStore(newTestSQLiteDB(t), testSQLLoginID)
	if err := store.ensureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.save(context.Background(), &sharedProfileRow{
		Identifier:    "mailto:profile-sync@example.invalid",
		DisplayName:   "Stored profile",
		RecordKey:     "synthetic-record-key",
		DecryptionKey: []byte("synthetic-decryption-key"),
		UpdatedTS:     0,
	}); err != nil {
		t.Fatal(err)
	}
	// Leave sharedProfiles empty. The worker's cached-profile pass therefore
	// has no Matrix work, while the real database refresh path still sees the
	// stale row.
	return &IMClient{sharedProfileStore: store}, store
}

func waitForProfileSyncSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func TestLocalSharedProfileRefreshWaitsFullIntervalAfterStartup(t *testing.T) {
	c, _ := newLocalProfileSyncTestClient(t)
	stop := make(chan struct{})
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var calls atomic.Int32
	fetcher := testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-releaseFirst
		case 2:
			close(secondStarted)
		}
		return rustpushgo.WrappedProfileRecord{}, errors.New("synthetic transient failure")
	})

	done := make(chan struct{})
	var stopOnce, releaseOnce sync.Once
	go func() {
		defer close(done)
		c.runLocalSharedProfileRefresh(zerolog.Nop(), stop, fetcher, 80*time.Millisecond)
	}()
	t.Cleanup(func() {
		stopOnce.Do(func() { close(stop) })
		releaseOnce.Do(func() { close(releaseFirst) })
		waitForProfileSyncSignal(t, done, "profile refresh worker leaked after test cleanup")
	})
	waitForProfileSyncSignal(t, firstStarted, "startup profile refresh did not begin")
	time.Sleep(140 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("startup refresh overlapped periodic refresh: calls=%d", got)
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	select {
	case <-secondStarted:
		t.Fatal("periodic refresh ran without waiting a full interval after startup")
	case <-time.After(35 * time.Millisecond):
	}
	select {
	case <-secondStarted:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("periodic refresh did not run after the post-startup interval")
	}
	stopOnce.Do(func() { close(stop) })
	waitForProfileSyncSignal(t, done, "profile refresh worker did not stop")
}

func TestLocalSharedProfileRefreshDropsFetchResultAfterStop(t *testing.T) {
	c, store := newLocalProfileSyncTestClient(t)
	stop := make(chan struct{})
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fetcher := testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
		calls.Add(1)
		close(started)
		<-release
		return rustpushgo.WrappedProfileRecord{DisplayName: "Disconnected result"}, nil
	})

	done := make(chan struct{})
	var stopOnce, releaseOnce sync.Once
	go func() {
		defer close(done)
		c.runLocalSharedProfileRefresh(zerolog.Nop(), stop, fetcher, time.Hour)
	}()
	t.Cleanup(func() {
		stopOnce.Do(func() { close(stop) })
		releaseOnce.Do(func() { close(release) })
		waitForProfileSyncSignal(t, done, "profile refresh worker leaked after test cleanup")
	})
	waitForProfileSyncSignal(t, started, "startup profile fetch did not begin")
	stopOnce.Do(func() { close(stop) })
	releaseOnce.Do(func() { close(release) })
	waitForProfileSyncSignal(t, done, "profile refresh worker did not exit after the in-flight fetch returned")

	rows, err := store.loadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored rows=%d, want 1", len(rows))
	}
	if rows[0].DisplayName != "Stored profile" || rows[0].UpdatedTS != 0 {
		t.Fatalf("disconnected result persisted: name=%q updated_ts=%d", rows[0].DisplayName, rows[0].UpdatedTS)
	}
	if _, ok := c.sharedProfiles.Load(rows[0].Identifier); ok {
		t.Fatal("disconnected result entered the in-memory cache")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls=%d, want 1", got)
	}
}

func TestLocalSharedProfileRefreshUsesCapturedStopChannel(t *testing.T) {
	c, _ := newLocalProfileSyncTestClient(t)
	oldStop := make(chan struct{})
	newStop := make(chan struct{})
	c.stopChan = oldStop
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fetcher := testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return rustpushgo.WrappedProfileRecord{}, errors.New("synthetic transient failure")
	})

	done := make(chan struct{})
	var oldStopOnce, newStopOnce, releaseOnce sync.Once
	go func() {
		defer close(done)
		c.runLocalSharedProfileRefresh(zerolog.Nop(), oldStop, fetcher, 40*time.Millisecond)
	}()
	t.Cleanup(func() {
		oldStopOnce.Do(func() { close(oldStop) })
		newStopOnce.Do(func() { close(newStop) })
		releaseOnce.Do(func() { close(release) })
		waitForProfileSyncSignal(t, done, "profile refresh worker leaked after test cleanup")
	})
	waitForProfileSyncSignal(t, started, "old connection's profile fetch did not begin")
	c.stopChan = newStop
	oldStopOnce.Do(func() { close(oldStop) })
	releaseOnce.Do(func() { close(release) })
	waitForProfileSyncSignal(t, done, "old worker followed the replacement connection's stop channel")
	time.Sleep(90 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("old connection performed another fetch: calls=%d", got)
	}
	newStopOnce.Do(func() { close(newStop) })
}

func TestLocalSharedProfileRefreshRetriesStartupThrottleAndSkipsFreshRow(t *testing.T) {
	c, store := newLocalProfileSyncTestClient(t)
	stop := make(chan struct{})
	secondSucceeded := make(chan struct{})
	var calls atomic.Int32
	var successOnce sync.Once
	fetcher := testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
		if calls.Add(1) == 1 {
			return rustpushgo.WrappedProfileRecord{}, errors.New("synthetic TooManyRequests")
		}
		successOnce.Do(func() { close(secondSucceeded) })
		return rustpushgo.WrappedProfileRecord{DisplayName: "Stored profile"}, nil
	})

	done := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		defer close(done)
		c.runLocalSharedProfileRefresh(zerolog.Nop(), stop, fetcher, 30*time.Millisecond)
	}()
	t.Cleanup(func() {
		stopOnce.Do(func() { close(stop) })
		waitForProfileSyncSignal(t, done, "profile refresh worker leaked after test cleanup")
	})
	waitForProfileSyncSignal(t, secondSucceeded, "periodic refresh did not retry the startup throttle")

	// Let several more worker intervals elapse. The successful unchanged
	// fetch must still mark the row fresh, so they should make no Apple calls.
	time.Sleep(110 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("fresh profile was fetched again: calls=%d, want 2", got)
	}
	rows, err := store.loadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].DisplayName != "Stored profile" || rows[0].UpdatedTS == 0 {
		t.Fatalf("recovered profile was not persisted as fresh: %+v", rows)
	}
	stopOnce.Do(func() { close(stop) })
	waitForProfileSyncSignal(t, done, "profile refresh worker did not stop")
}

func TestRefreshAllSharedProfilesReturnsWithNilClient(t *testing.T) {
	c, store := newLocalProfileSyncTestClient(t)
	c.stopChan = make(chan struct{})

	c.refreshAllSharedProfiles(zerolog.Nop())

	rows, err := store.loadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].DisplayName != "Stored profile" || rows[0].UpdatedTS != 0 {
		t.Fatalf("nil-client refresh changed stored row: %+v", rows)
	}
}
