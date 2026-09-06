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

func loadSingleSharedProfile(t *testing.T, store *sharedProfileStore) *sharedProfileRow {
	t.Helper()
	rows, err := store.loadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored rows=%d, want 1", len(rows))
	}
	return rows[0]
}

func cachedSharedProfileRow(t *testing.T, c *IMClient, identifier string) *sharedProfileRow {
	t.Helper()
	value, ok := c.sharedProfiles.Load(identifier)
	if !ok {
		t.Fatal("shared profile missing from cache")
	}
	row, ok := value.(*sharedProfileRow)
	if !ok {
		t.Fatalf("cached shared profile has type %T", value)
	}
	return row
}

func TestSharedProfileRefreshDoesNotReplaceNewerIncomingProfile(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		newKey           string
		newDecryptionKey []byte
		newDisplayName   string
	}{
		{name: "new keys", newKey: "new-record-key", newDecryptionKey: []byte("newer-decryption-key"), newDisplayName: "Newer incoming profile"},
		{name: "same keys and content", newKey: "synthetic-record-key", newDecryptionKey: []byte("synthetic-decryption-key"), newDisplayName: "Stored profile"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			c, store := newLocalProfileSyncTestClient(t)
			stored := loadSingleSharedProfile(t, store)
			c.cacheSharedProfileIfAbsent(stored)

			started := make(chan struct{})
			release := make(chan struct{})
			fetcher := testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
				close(started)
				<-release
				// An unchanged result still publishes a fresh timestamp and would
				// overwrite the incoming row if supersession were checked by keys.
				return rustpushgo.WrappedProfileRecord{DisplayName: "Stored profile"}, nil
			})

			done := make(chan struct{})
			go func() {
				defer close(done)
				c.refreshAllSharedProfilesForConnection(zerolog.Nop(), make(chan struct{}), fetcher, 0)
			}()
			waitForProfileSyncSignal(t, started, "background profile fetch did not begin")

			incoming := &sharedProfileRow{
				Identifier:    stored.Identifier,
				DisplayName:   testCase.newDisplayName,
				RecordKey:     testCase.newKey,
				DecryptionKey: testCase.newDecryptionKey,
				UpdatedTS:     time.Now().Add(time.Minute).UnixMilli(),
			}
			if err := c.publishSharedProfile(incoming); err != nil {
				t.Fatal(err)
			}
			incomingToken := cachedSharedProfileRow(t, c, stored.Identifier)
			close(release)
			waitForProfileSyncSignal(t, done, "background profile refresh did not finish")

			persisted := loadSingleSharedProfile(t, store)
			if persisted.RecordKey != incoming.RecordKey ||
				persisted.DisplayName != incoming.DisplayName ||
				persisted.UpdatedTS != incoming.UpdatedTS {
				t.Fatalf("background result replaced newer persisted profile: %+v", persisted)
			}
			if cached := cachedSharedProfileRow(t, c, stored.Identifier); cached != incomingToken {
				t.Fatalf("background result replaced newer cache token: got %p, want %p", cached, incomingToken)
			}
		})
	}
}

func TestSharedProfileRefreshUsesNewerProfileBeforeLaterFetch(t *testing.T) {
	c, store := newLocalProfileSyncTestClient(t)
	ctx := context.Background()
	secondIdentifier := "mailto:second-profile-sync@example.invalid"
	if err := store.save(ctx, &sharedProfileRow{
		Identifier:    secondIdentifier,
		DisplayName:   "Second stored profile",
		RecordKey:     "second-old-key",
		DecryptionKey: []byte("second-old-decryption-key"),
		UpdatedTS:     0,
	}); err != nil {
		t.Fatal(err)
	}

	started := make(chan string, 1)
	release := make(chan struct{})
	var mu sync.Mutex
	var fetchedKeys []string
	fetcher := testSharedProfileFetcher(func(recordKey string, _ []byte, _ bool) (rustpushgo.WrappedProfileRecord, error) {
		mu.Lock()
		fetchedKeys = append(fetchedKeys, recordKey)
		call := len(fetchedKeys)
		mu.Unlock()
		if call == 1 {
			started <- recordKey
			<-release
			return rustpushgo.WrappedProfileRecord{}, errors.New("synthetic transient failure")
		}
		return rustpushgo.WrappedProfileRecord{}, errors.New("unexpected fetch of newer fresh profile")
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.refreshAllSharedProfilesForConnection(zerolog.Nop(), make(chan struct{}), fetcher, 0)
	}()
	var firstKey string
	select {
	case firstKey = <-started:
	case <-time.After(time.Second):
		t.Fatal("first profile fetch did not begin")
	}

	newerIdentifier := secondIdentifier
	if firstKey == "second-old-key" {
		newerIdentifier = "mailto:profile-sync@example.invalid"
	}
	incoming := &sharedProfileRow{
		Identifier:    newerIdentifier,
		DisplayName:   "Newer incoming profile",
		RecordKey:     "newer-before-fetch-key",
		DecryptionKey: []byte("newer-before-fetch-decryption-key"),
		UpdatedTS:     time.Now().UnixMilli(),
	}
	if err := c.publishSharedProfile(incoming); err != nil {
		t.Fatal(err)
	}
	incomingToken := cachedSharedProfileRow(t, c, newerIdentifier)
	close(release)
	waitForProfileSyncSignal(t, done, "profile refresh did not finish")

	mu.Lock()
	defer mu.Unlock()
	if len(fetchedKeys) != 1 {
		t.Fatalf("fetched stale keys for a newer fresh profile: %v", fetchedKeys)
	}
	if cached := cachedSharedProfileRow(t, c, newerIdentifier); cached != incomingToken {
		t.Fatalf("stale DB hydration replaced newer cache token: got %p, want %p", cached, incomingToken)
	}
}

func TestSharedProfileHydrationDoesNotReplaceNewerCacheToken(t *testing.T) {
	c, store := newLocalProfileSyncTestClient(t)
	stale := loadSingleSharedProfile(t, store)
	incoming := &sharedProfileRow{
		Identifier:    stale.Identifier,
		DisplayName:   "Newer incoming profile",
		RecordKey:     "newer-record-key",
		DecryptionKey: []byte("newer-decryption-key"),
		UpdatedTS:     time.Now().UnixMilli(),
	}
	if err := c.publishSharedProfile(incoming); err != nil {
		t.Fatal(err)
	}
	incomingToken := cachedSharedProfileRow(t, c, stale.Identifier)
	if canonical := c.cacheSharedProfileIfAbsent(stale); canonical != incomingToken {
		t.Fatalf("stale hydration replaced newer token: got %p, want %p", canonical, incomingToken)
	}
	if cached := cachedSharedProfileRow(t, c, stale.Identifier); cached != incomingToken {
		t.Fatalf("stale hydration replaced cached row: %+v", cached)
	}
}

func TestSharedProfilePublicationFailurePreservesRetryableCacheState(t *testing.T) {
	c, store := newLocalProfileSyncTestClient(t)
	ctx := context.Background()
	stored := loadSingleSharedProfile(t, store)
	snapshot := c.cacheSharedProfileIfAbsent(stored)
	if _, err := store.db.Exec(ctx, `CREATE TRIGGER reject_shared_profile_update
		BEFORE UPDATE ON shared_profiles
		BEGIN
			SELECT RAISE(FAIL, 'synthetic profile save failure');
		END`); err != nil {
		t.Fatal(err)
	}

	background := cloneSharedProfileRow(snapshot)
	background.DisplayName = "Background result"
	background.UpdatedTS = time.Now().UnixMilli()
	published, err := c.publishRefreshedSharedProfile(snapshot, background)
	if published || err == nil {
		t.Fatalf("background publication=(%v, %v), want rejected save and error", published, err)
	}
	if cached := cachedSharedProfileRow(t, c, stored.Identifier); cached != snapshot || cached.UpdatedTS != 0 {
		t.Fatalf("failed background save changed retryable cache state: %+v", cached)
	}

	incoming := &sharedProfileRow{
		Identifier:    stored.Identifier,
		DisplayName:   "Incoming result",
		RecordKey:     "incoming-record-key",
		DecryptionKey: []byte("incoming-decryption-key"),
		UpdatedTS:     time.Now().UnixMilli(),
	}
	if err := c.publishSharedProfile(incoming); err == nil {
		t.Fatal("incoming publication unexpectedly persisted")
	}
	cached := cachedSharedProfileRow(t, c, stored.Identifier)
	if cached.DisplayName != incoming.DisplayName || cached.RecordKey != incoming.RecordKey {
		t.Fatalf("failed incoming save discarded fetched profile: %+v", cached)
	}
	if cached.UpdatedTS != 0 {
		t.Fatalf("failed incoming save marked profile fresh: updated_ts=%d", cached.UpdatedTS)
	}
	if persisted := loadSingleSharedProfile(t, store); persisted.RecordKey != stored.RecordKey || persisted.DisplayName != stored.DisplayName {
		t.Fatalf("failed incoming save changed persisted profile: %+v", persisted)
	}
	if _, err := store.db.Exec(ctx, `DROP TRIGGER reject_shared_profile_update`); err != nil {
		t.Fatal(err)
	}
	var fetchedKey string
	fetcher := testSharedProfileFetcher(func(recordKey string, _ []byte, _ bool) (rustpushgo.WrappedProfileRecord, error) {
		fetchedKey = recordKey
		return rustpushgo.WrappedProfileRecord{DisplayName: incoming.DisplayName}, nil
	})
	c.refreshAllSharedProfilesForConnection(zerolog.Nop(), make(chan struct{}), fetcher, 0)
	if fetchedKey != incoming.RecordKey {
		t.Fatalf("retry fetched record key %q, want %q", fetchedKey, incoming.RecordKey)
	}
	persisted := loadSingleSharedProfile(t, store)
	if persisted.RecordKey != incoming.RecordKey || persisted.DisplayName != incoming.DisplayName || persisted.UpdatedTS == 0 {
		t.Fatalf("later refresh did not persist incoming profile: %+v", persisted)
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
	cached := cachedSharedProfileRow(t, c, rows[0].Identifier)
	if cached.DisplayName != "Stored profile" || cached.UpdatedTS != 0 {
		t.Fatalf("disconnected result entered the in-memory cache: %+v", cached)
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
