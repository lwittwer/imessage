package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

func TestSharedProfileRefreshCooldownAndRecovery(t *testing.T) {
	c, store := newLocalProfileSyncTestClient(t)
	calls := 0
	fetcher := testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
		calls++
		if calls == 1 {
			return rustpushgo.WrappedProfileRecord{}, errors.New("synthetic TooManyRequests")
		}
		return rustpushgo.WrappedProfileRecord{DisplayName: "Stored profile"}, nil
	})
	refresh := func() { c.refreshAllSharedProfilesForConnection(zerolog.Nop(), nil, fetcher, 0) }
	refresh()
	refresh()
	if calls != 1 {
		t.Fatalf("cooldown allowed another fetch: %d", calls)
	}
	row := cachedSharedProfileRow(t, c, loadSingleSharedProfile(t, store).Identifier)
	until := c.sharedProfileCooldownUntil
	if c.sharedProfileRefreshEligible(row, until.Add(-time.Nanosecond)) || !c.sharedProfileRefreshEligible(row, until) {
		t.Fatal("incorrect cooldown boundary")
	}
	c.sharedProfileCooldownUntil = time.Now().Add(-time.Second)
	refresh()
	refresh()
	if calls != 2 {
		t.Fatalf("recovery/freshness calls=%d, want 2", calls)
	}
	if loadSingleSharedProfile(t, store).UpdatedTS == 0 {
		t.Fatal("successful retry not marked fresh")
	}
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

func TestSharedProfileMissingRecordBackoff(t *testing.T) {
	c := &IMClient{}
	row := c.cacheSharedProfileIfAbsent(&sharedProfileRow{Identifier: "synthetic", RecordKey: "old"})
	now := time.Unix(1000, 0)
	missing := errors.New(sharedProfileMissingRecordError)
	for _, delay := range []time.Duration{6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 24 * time.Hour} {
		c.recordSharedProfileFetchResult(row, missing, now)
		if c.sharedProfileRefreshEligible(row, now.Add(delay-time.Nanosecond)) {
			t.Fatal("retry eligible too soon")
		}
		now = now.Add(delay)
		if !c.sharedProfileRefreshEligible(row, now) {
			t.Fatal("bounded retry never became eligible")
		}
	}
	healthy := c.cacheSharedProfileIfAbsent(&sharedProfileRow{Identifier: "healthy"})
	if !c.sharedProfileRefreshEligible(healthy, now.Add(-time.Hour)) {
		t.Fatal("failing record delayed healthy record")
	}
	c.recordSharedProfileFetchResult(row, nil, now)
	c.recordSharedProfileFetchResult(row, missing, now)
	if c.sharedProfileRetries[row.Identifier].delay != sharedProfileRetryInitial {
		t.Fatal("success did not reset backoff")
	}
	c.recordSharedProfileFetchResult(row, errors.New("connection reset by peer"), now)
	if !c.sharedProfileRefreshEligible(row, now) {
		t.Fatal("transient error treated as missing record")
	}
}

func TestSharedProfileRetryNewAnnouncement(t *testing.T) {
	for _, change := range []string{"record key", "decryption key", "same keys"} {
		t.Run(change, func(t *testing.T) {
			c := &IMClient{}
			row := c.cacheSharedProfileIfAbsent(&sharedProfileRow{Identifier: "synthetic", RecordKey: "old", DecryptionKey: []byte("old")})
			now := time.Now()
			missing := errors.New(sharedProfileMissingRecordError)
			c.recordSharedProfileFetchResult(row, missing, now)
			replacement := cloneSharedProfileRow(row)
			if change == "record key" {
				replacement.RecordKey = "new"
			}
			if change == "decryption key" {
				replacement.DecryptionKey = []byte("new")
			}
			if err := c.publishSharedProfile(replacement); err != nil {
				t.Fatal(err)
			}
			current := cachedSharedProfileRow(t, c, row.Identifier)
			c.recordSharedProfileFetchResult(row, missing, now) // late old failure
			if !c.sharedProfileRefreshEligible(current, now) {
				t.Fatal("new announcement inherited old failure")
			}
		})
	}
}

func TestSharedProfileFailedFetchPreservesCache(t *testing.T) {
	for _, fetchErr := range []error{errors.New(sharedProfileMissingRecordError), errors.New("connection reset by peer"), errors.New("TooManyRequests")} {
		t.Run(fetchErr.Error(), func(t *testing.T) {
			c, store := newLocalProfileSyncTestClient(t)
			original := loadSingleSharedProfile(t, store)
			original.FirstName, original.LastName = "Stored", "Person"
			original.Avatar = []byte("synthetic avatar")
			if err := store.save(context.Background(), original); err != nil {
				t.Fatal(err)
			}
			c.refreshAllSharedProfilesForConnection(zerolog.Nop(), nil, testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
				return rustpushgo.WrappedProfileRecord{}, fetchErr
			}), 0)
			if !reflect.DeepEqual(original, loadSingleSharedProfile(t, store)) || !reflect.DeepEqual(original, cachedSharedProfileRow(t, c, original.Identifier)) {
				t.Fatal("failed fetch changed cached or persisted profile")
			}
		})
	}
}

func TestSharedProfileRetrySkipsOnlyFailingRecord(t *testing.T) {
	c, store := newLocalProfileSyncTestClient(t)
	row := c.cacheSharedProfileIfAbsent(loadSingleSharedProfile(t, store))
	c.recordSharedProfileFetchResult(row, errors.New(sharedProfileMissingRecordError), time.Now())
	healthy := &sharedProfileRow{Identifier: "healthy", RecordKey: "healthy-key", DecryptionKey: []byte("synthetic"), DisplayName: "Healthy"}
	if err := store.save(context.Background(), healthy); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.refreshAllSharedProfilesForConnection(zerolog.Nop(), nil, testSharedProfileFetcher(func(key string, _ []byte, _ bool) (rustpushgo.WrappedProfileRecord, error) {
		calls++
		if key != healthy.RecordKey {
			t.Fatal("retried backed-off record")
		}
		return rustpushgo.WrappedProfileRecord{DisplayName: healthy.DisplayName}, nil
	}), 0)
	if calls != 1 {
		t.Fatalf("healthy calls=%d, want 1", calls)
	}
}

func TestSharedProfileRateLimitStopsPass(t *testing.T) {
	c, store := newLocalProfileSyncTestClient(t)
	if err := store.save(context.Background(), &sharedProfileRow{Identifier: "second", RecordKey: "second-key", DecryptionKey: []byte("synthetic")}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetcher := testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
		calls++
		return rustpushgo.WrappedProfileRecord{}, errors.New("TooManyRequests")
	})
	for range 2 {
		c.refreshAllSharedProfilesForConnection(zerolog.Nop(), nil, fetcher, 0)
	}
	if calls != 1 {
		t.Fatalf("429 did not stop and defer background pass: calls=%d", calls)
	}
}

func TestSharedProfileFailureAfterStopSurvivesReconnect(t *testing.T) {
	for _, fetchErr := range []error{errors.New(sharedProfileMissingRecordError), errors.New("TooManyRequests")} {
		t.Run(fetchErr.Error(), func(t *testing.T) {
			c, store := newLocalProfileSyncTestClient(t)
			original := loadSingleSharedProfile(t, store)
			stop := make(chan struct{})
			c.refreshAllSharedProfilesForConnection(zerolog.Nop(), stop, testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
				close(stop)
				return rustpushgo.WrappedProfileRecord{}, fetchErr
			}), 0)
			c.refreshAllSharedProfilesForConnection(zerolog.Nop(), make(chan struct{}), testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
				t.Fatal("reconnected worker forgot completed fetch failure")
				return rustpushgo.WrappedProfileRecord{}, nil
			}), 0)
			if !reflect.DeepEqual(original, loadSingleSharedProfile(t, store)) || !reflect.DeepEqual(original, cachedSharedProfileRow(t, c, original.Identifier)) {
				t.Fatal("disconnected failure changed cached or persisted profile")
			}
		})
	}
}

func TestSharedProfileBackoffPassIsLogged(t *testing.T) {
	for _, fetchErr := range []error{errors.New(sharedProfileMissingRecordError), errors.New("TooManyRequests")} {
		t.Run(fetchErr.Error(), func(t *testing.T) {
			c, store := newLocalProfileSyncTestClient(t)
			row := c.cacheSharedProfileIfAbsent(loadSingleSharedProfile(t, store))
			c.recordSharedProfileFetchResult(row, fetchErr, time.Now())
			var output bytes.Buffer
			log := zerolog.New(&output).Level(zerolog.DebugLevel)
			c.refreshAllSharedProfilesForConnection(log, nil, testSharedProfileFetcher(func(string, []byte, bool) (rustpushgo.WrappedProfileRecord, error) {
				t.Fatal("backed-off pass made a request")
				return rustpushgo.WrappedProfileRecord{}, nil
			}), 0)
			var event struct {
				Message string
				Skipped int `json:"skipped_backoff"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
				t.Fatalf("missing pass log: %v", err)
			}
			if event.Message != "Periodic shared-profile sync completed" || event.Skipped != 1 {
				t.Fatalf("unexpected pass log: %+v", event)
			}
		})
	}
}
