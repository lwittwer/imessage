package connector

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func createScrubberBridgeMessageTable(t *testing.T, db *dbutil.Database, ctx context.Context) {
	t.Helper()
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS message (
		id TEXT NOT NULL,
		bridge_id TEXT NOT NULL,
		room_id TEXT NOT NULL,
		room_receiver TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create message table: %v", err)
	}
	if _, err := db.Exec(ctx, `CREATE INDEX IF NOT EXISTS message_delivery_test_idx
		ON message (bridge_id, room_receiver, id)`); err != nil {
		t.Fatalf("create message index: %v", err)
	}
}

func insertScrubberBridgeMessage(t *testing.T, db *dbutil.Database, ctx context.Context, id, bridgeID, portalID, receiver string) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO message (id, bridge_id, room_id, room_receiver) VALUES ($1, $2, $3, $4)`,
		id, bridgeID, portalID, receiver,
	); err != nil {
		t.Fatalf("insert bridgev2 message row %q: %v", id, err)
	}
}

// TestEnsureSchemaCreatesScrubIndex verifies that the privacy scrubbers can
// find old, un-scrubbed rows without scanning the entire cloud_message table.
func TestEnsureSchemaCreatesScrubIndex(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	var indexSQL string
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type='index' AND name='cloud_message_scrub_cover_idx'`,
	).Scan(&indexSQL); err != nil {
		t.Fatalf("read cloud_message_scrub_cover_idx: %v", err)
	}
	if !strings.Contains(indexSQL, "WHERE body_scrubbed") {
		t.Fatalf("cloud_message_scrub_cover_idx is not partial: %s", indexSQL)
	}

	// A database that already carries the earlier two-column index must end
	// up with only the covering one.
	if _, err := db.Exec(ctx, `CREATE INDEX cloud_message_scrub_idx
		ON cloud_message (login_id, updated_ts) WHERE body_scrubbed=FALSE`); err != nil {
		t.Fatalf("create legacy index: %v", err)
	}
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("second ensureSchema: %v", err)
	}
	var legacy int
	if err := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='cloud_message_scrub_idx'`,
	).Scan(&legacy); err != nil {
		t.Fatalf("count legacy index: %v", err)
	}
	if legacy != 0 {
		t.Fatal("legacy cloud_message_scrub_idx survived ensureSchema")
	}

	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: "plan-check-guid", PortalID: "gid:plan", TimestampMS: 1,
		Service: "iMessage",
	}}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}
	if _, err := db.Exec(ctx, `ANALYZE`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
	rows, err := db.Query(ctx,
		`EXPLAIN QUERY PLAN SELECT guid, COALESCE(deleted, FALSE), COALESCE(portal_id, '') FROM cloud_message
		 WHERE login_id=$1 AND body_scrubbed=FALSE
		   AND (tapback_type IS NULL OR tapback_type < 2000) AND updated_ts < $2
		 ORDER BY updated_ts ASC`,
		testSQLLoginID, time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan rows: %v", err)
	}
	if got := plan.String(); !strings.Contains(got, "COVERING INDEX cloud_message_scrub_cover_idx") {
		t.Fatalf("candidate query is not served from cloud_message_scrub_cover_idx alone; plan:\n%s", got)
	}
}

func TestLoadBridgedMessageIDsNormalizesAndScopesIDs(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	createScrubberBridgeMessageTable(t, db, ctx)

	otherLogin := networkid.UserLoginID("other-login")
	const portalID = "gid:test"
	insertScrubberBridgeMessage(t, db, ctx, "ABC-1", "bridge", portalID, string(testSQLLoginID))
	insertScrubberBridgeMessage(t, db, ctx, "guid-2_att0", "bridge", portalID, "")
	insertScrubberBridgeMessage(t, db, ctx, strings.ToUpper("guid-3"), "bridge", portalID, string(testSQLLoginID))
	insertScrubberBridgeMessage(t, db, ctx, "wrong-bridge", "other-bridge", portalID, string(testSQLLoginID))
	insertScrubberBridgeMessage(t, db, ctx, "wrong-login", "bridge", portalID, string(otherLogin))

	witnesses, err := store.loadBridgedMessageIDs(ctx, "bridge")
	if err != nil {
		t.Fatalf("loadBridgedMessageIDs: %v", err)
	}
	for guid, want := range map[string]string{
		"abc-1":        "ABC-1",
		"guid-2":       "guid-2_att0",
		"guid-3":       strings.ToUpper("guid-3"),
		"wrong-bridge": "",
		"wrong-login":  "",
	} {
		got := witnesses[bridgedDeliveryKey{guid: guid, portalID: portalID}]
		if got != want {
			t.Errorf("witness for %q = %q, want %q", guid, got, want)
		}
	}
}

func TestScrubBridgedBodiesMultiChunkPreservesEligibility(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	createScrubberBridgeMessageTable(t, db, ctx)

	const bridgeID = "test-bridge"
	now := time.Now().UnixMilli()
	old := now - int64(time.Hour/time.Millisecond)

	bulk := make([]cloudMessageRow, 0, 2500)
	for i := 0; i < 2500; i++ {
		guid := fmt.Sprintf("delivered-%04d", i)
		bulk = append(bulk, cloudMessageRow{
			GUID: guid, PortalID: "gid:bulk", TimestampMS: old,
			Text: "secret " + guid, Sender: "tel:+1555", Service: "iMessage", HasBody: true,
		})
		insertScrubberBridgeMessage(t, db, ctx, guid, bridgeID, "gid:bulk", string(testSQLLoginID))
	}
	if err := store.upsertMessageBatch(ctx, bulk); err != nil {
		t.Fatalf("upsert bulk messages: %v", err)
	}

	tapbackType := uint32(2001)
	special := []cloudMessageRow{
		{GUID: "fresh-delivered", PortalID: "gid:bulk", TimestampMS: now,
			Text: "fresh secret", Service: "iMessage", HasBody: true},
		{GUID: "undelivered-old", PortalID: "gid:bulk", TimestampMS: old,
			Text: "undelivered secret", Service: "iMessage", HasBody: true},
		{GUID: "tapback-old", PortalID: "gid:bulk", TimestampMS: old,
			Text: "Loved 'x'", Service: "iMessage", TapbackType: &tapbackType},
		{GUID: "deleted-old", PortalID: "gid:bulk", TimestampMS: old,
			Text: "deleted secret", Deleted: true, Service: "iMessage", HasBody: true},
		{GUID: "restore-portal-row", PortalID: "gid:restore", TimestampMS: old,
			Text: "restoring secret", Service: "iMessage", HasBody: true},
	}
	insertScrubberBridgeMessage(t, db, ctx, "fresh-delivered", bridgeID, "gid:bulk", string(testSQLLoginID))
	insertScrubberBridgeMessage(t, db, ctx, "restore-portal-row", bridgeID, "gid:restore", string(testSQLLoginID))
	if err := store.upsertMessageBatch(ctx, special); err != nil {
		t.Fatalf("upsert special messages: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$1 WHERE login_id=$2 AND guid <> $3`,
		old, testSQLLoginID, "fresh-delivered",
	); err != nil {
		t.Fatalf("age messages: %v", err)
	}

	textOf := func(guid string) sql.NullString {
		t.Helper()
		var text sql.NullString
		if err := db.QueryRow(ctx,
			`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
			testSQLLoginID, guid,
		).Scan(&text); err != nil {
			t.Fatalf("read text of %s: %v", guid, err)
		}
		return text
	}

	total, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, []string{"gid:restore"})
	if err != nil {
		t.Fatalf("scrubBridgedBodies: %v", err)
	}
	if total != 2501 {
		t.Fatalf("scrubbed %d rows, want 2501", total)
	}
	for _, guid := range []string{"fresh-delivered", "undelivered-old", "restore-portal-row", "tapback-old"} {
		if text := textOf(guid); !text.Valid || text.String == "" {
			t.Errorf("%s text was cleared, want preserved", guid)
		}
	}
	if text := textOf("deleted-old"); text.Valid && text.String != "" {
		t.Errorf("deleted-old text = %q, want NULL", text.String)
	}

	again, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, []string{"gid:restore"})
	if err != nil {
		t.Fatalf("second scrubBridgedBodies: %v", err)
	}
	if again != 0 {
		t.Fatalf("second pass scrubbed %d rows, want 0", again)
	}
}

func TestScrubBridgedBodiesRechecksDeliveryBetweenChunks(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	createScrubberBridgeMessageTable(t, db, ctx)

	const (
		bridgeID = "test-bridge"
		lateGUID = "delivered-late"
	)
	old := time.Now().Add(-time.Hour).UnixMilli()
	rows := make([]cloudMessageRow, 0, 1001)
	for i := 0; i < 1000; i++ {
		guid := fmt.Sprintf("forced-%04d", i)
		rows = append(rows, cloudMessageRow{
			GUID: guid, PortalID: "gid:forced", TimestampMS: old,
			Text: "forced secret", Deleted: true, Service: "iMessage", HasBody: true,
		})
	}
	rows = append(rows, cloudMessageRow{
		GUID: lateGUID, PortalID: "gid:late", TimestampMS: old,
		Text: "must survive", Service: "iMessage", HasBody: true,
	})
	if err := store.upsertMessageBatch(ctx, rows); err != nil {
		t.Fatalf("upsert messages: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$1 WHERE login_id=$2 AND guid LIKE 'forced-%'`,
		old, testSQLLoginID,
	); err != nil {
		t.Fatalf("age forced messages: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$1 WHERE login_id=$2 AND guid=$3`,
		old+1, testSQLLoginID, lateGUID,
	); err != nil {
		t.Fatalf("age delivered message: %v", err)
	}
	insertScrubberBridgeMessage(t, db, ctx, lateGUID, bridgeID, "gid:late", string(testSQLLoginID))

	// The first UPDATE contains exactly the 1000 forced rows. Delete the live
	// delivery witness from inside that statement so the second batch must
	// observe the deletion without timing hooks or sleeps.
	if _, err := db.Exec(ctx, `
		CREATE TRIGGER delete_late_delivery
		AFTER UPDATE OF body_scrubbed ON cloud_message
		WHEN OLD.guid='forced-0999'
		BEGIN
			DELETE FROM message WHERE id='delivered-late';
		END
	`); err != nil {
		t.Fatalf("create deletion trigger: %v", err)
	}

	total, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, nil)
	if err != nil {
		t.Fatalf("scrubBridgedBodies: %v", err)
	}
	if total != 1000 {
		t.Fatalf("scrubbed %d rows, want only 1000 forced rows", total)
	}
	var text sql.NullString
	var scrubbed bool
	if err := db.QueryRow(ctx,
		`SELECT text, body_scrubbed FROM cloud_message WHERE login_id=$1 AND guid=$2`,
		testSQLLoginID, lateGUID,
	).Scan(&text, &scrubbed); err != nil {
		t.Fatalf("read delivered candidate: %v", err)
	}
	if !text.Valid || text.String != "must survive" || scrubbed {
		t.Fatalf("late candidate text=%q valid=%v scrubbed=%v, want preserved", text.String, text.Valid, scrubbed)
	}
	var bridgeRows int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM message WHERE id=$1`, lateGUID).Scan(&bridgeRows); err != nil {
		t.Fatalf("count bridge messages: %v", err)
	}
	if bridgeRows != 0 {
		t.Fatalf("bridge message count = %d, want 0 to prove trigger fired", bridgeRows)
	}
}

func TestScrubBridgedBodiesRequiresPortalScopedDelivery(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	createScrubberBridgeMessageTable(t, db, ctx)

	const (
		bridgeID = "test-bridge"
		portalID = "gid:current"
		guid     = "portal-scoped-guid"
	)
	old := time.Now().Add(-time.Hour).UnixMilli()
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: guid, PortalID: portalID, TimestampMS: old,
		Text: "must survive wrong-room proof", Service: "iMessage", HasBody: true,
	}}); err != nil {
		t.Fatalf("upsert message: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$1 WHERE login_id=$2 AND guid=$3`,
		old, testSQLLoginID, guid,
	); err != nil {
		t.Fatalf("age message: %v", err)
	}
	insertScrubberBridgeMessage(t, db, ctx, guid, bridgeID, "gid:wrong", string(testSQLLoginID))

	if total, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, nil); err != nil {
		t.Fatalf("scrub with wrong-room witness: %v", err)
	} else if total != 0 {
		t.Fatalf("scrubbed %d rows with only wrong-room proof, want 0", total)
	}
	var text sql.NullString
	if err := db.QueryRow(ctx,
		`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
		testSQLLoginID, guid,
	).Scan(&text); err != nil {
		t.Fatalf("read preserved body: %v", err)
	}
	if !text.Valid || text.String != "must survive wrong-room proof" {
		t.Fatalf("body after wrong-room proof = %q valid=%v, want preserved", text.String, text.Valid)
	}

	// A matching shared-receiver witness authorizes the same row even while the
	// wrong-room witness remains, proving that delivery is keyed by GUID+portal.
	insertScrubberBridgeMessage(t, db, ctx, guid, bridgeID, portalID, "")
	if total, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, nil); err != nil {
		t.Fatalf("scrub with matching-room witness: %v", err)
	} else if total != 1 {
		t.Fatalf("scrubbed %d rows with matching-room proof, want 1", total)
	}
}

func TestScrubBridgedBodiesPreservesKnownSourceRemap(t *testing.T) {
	for _, bridgeFiltered := range []bool{false, true} {
		t.Run(fmt.Sprintf("bridge_filtered=%v", bridgeFiltered), func(t *testing.T) {
			ctx := context.Background()
			db := newTestSQLiteDB(t)
			store := newCloudBackfillStore(db, testSQLLoginID, bridgeFiltered)
			if err := store.ensureSchema(ctx); err != nil {
				t.Fatalf("ensureSchema: %v", err)
			}
			createScrubberBridgeMessageTable(t, db, ctx)

			const (
				bridgeID  = "test-bridge"
				chatID    = "remapped-source-chat"
				oldPortal = "gid:old-portal"
				newPortal = "gid:new-portal"
				guid      = "remapped-source-guid"
			)
			now := time.Now().UnixMilli()
			old := now - int64(time.Hour/time.Millisecond)
			if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{{
				CloudChatID: chatID, PortalID: newPortal, Service: "iMessage",
				ParticipantsJSON: "[]", UpdatedTS: now,
			}}); err != nil {
				t.Fatalf("upsert remapped source: %v", err)
			}
			if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
				GUID: guid, PortalID: oldPortal, CloudChatID: chatID,
				TimestampMS: old, Text: "must remain recoverable", Service: "iMessage", HasBody: true,
			}}); err != nil {
				t.Fatalf("upsert stale message: %v", err)
			}
			if _, err := db.Exec(ctx,
				`UPDATE cloud_message SET updated_ts=$1 WHERE login_id=$2 AND guid=$3`,
				old, testSQLLoginID, guid,
			); err != nil {
				t.Fatalf("age stale message: %v", err)
			}
			insertScrubberBridgeMessage(t, db, ctx, guid, bridgeID, oldPortal, string(testSQLLoginID))

			total, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, nil)
			if err != nil {
				t.Fatalf("scrubBridgedBodies: %v", err)
			}
			if total != 0 {
				t.Fatalf("scrubbed %d rows, want 0 for known source remap", total)
			}
			var text sql.NullString
			if err := db.QueryRow(ctx,
				`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
				testSQLLoginID, guid,
			).Scan(&text); err != nil {
				t.Fatalf("read stale message: %v", err)
			}
			if !text.Valid || text.String != "must remain recoverable" {
				t.Fatalf("message text=%q valid=%v, want preserved", text.String, text.Valid)
			}
		})
	}
}

func TestScrubBatchRechecksPendingBackfillAtWriteTime(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	createScrubberBridgeMessageTable(t, db, ctx)

	now := time.Now().UnixMilli()
	old := now - int64(time.Hour/time.Millisecond)
	const portalID = "gid:newly-pending"
	const guid = "pending-race-guid"
	insertScrubberBridgeMessage(t, db, ctx, guid, "bridge", portalID, string(testSQLLoginID))
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: guid, PortalID: portalID, CloudChatID: "pending-race-chat",
		TimestampMS: old, Text: "must survive", Service: "iMessage", HasBody: true,
	}}); err != nil {
		t.Fatalf("upsert message: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$1 WHERE login_id=$2 AND guid=$3`,
		old, testSQLLoginID, guid,
	); err != nil {
		t.Fatalf("age message: %v", err)
	}

	cutoff := now - int64(time.Minute/time.Millisecond)
	candidates, err := store.scrubCandidates(ctx, cutoff, nil)
	if err != nil {
		t.Fatalf("scrubCandidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %v, want one row", candidates)
	}

	// The portal becomes known-pending after candidate enumeration but before
	// the UPDATE. The write-time gate must prevent stale candidate state from
	// deleting plaintext that the first forward backfill still needs.
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{{
		CloudChatID: "pending-race-chat", PortalID: portalID, Service: "iMessage",
		ParticipantsJSON: "[]", UpdatedTS: now,
	}}); err != nil {
		t.Fatalf("upsert pending chat: %v", err)
	}
	scrubbed, err := store.scrubBatchIfEligible(ctx, "bridge", cutoff,
		map[bridgedDeliveryKey]string{{guid: guid, portalID: portalID}: guid}, candidates)
	if err != nil {
		t.Fatalf("scrubBatchIfEligible: %v", err)
	}
	if scrubbed != 0 {
		t.Fatalf("scrubbed %d rows, want 0 after portal became pending", scrubbed)
	}
	var text sql.NullString
	if err := db.QueryRow(ctx,
		`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
		testSQLLoginID, guid,
	).Scan(&text); err != nil {
		t.Fatalf("read message text: %v", err)
	}
	if !text.Valid || text.String != "must survive" {
		t.Fatalf("message text = %q (valid=%v), want preserved", text.String, text.Valid)
	}
}

func TestScrubBatchRechecksPermanentFilterAtWriteTime(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	now := time.Now().UnixMilli()
	old := now - int64(time.Hour/time.Millisecond)
	const (
		chatID   = "filtered-race-chat"
		portalID = "gid:filtered-race"
		guid     = "filtered-race-guid"
	)
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{{
		CloudChatID: chatID, PortalID: portalID, Service: "iMessage",
		ParticipantsJSON: "[]", IsFiltered: 1, UpdatedTS: now,
	}}); err != nil {
		t.Fatalf("upsert filtered chat: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: guid, PortalID: portalID, CloudChatID: chatID,
		TimestampMS: old, Text: "must remain recoverable", Service: "iMessage", HasBody: true,
	}}); err != nil {
		t.Fatalf("upsert message: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$1 WHERE login_id=$2 AND guid=$3`,
		old, testSQLLoginID, guid,
	); err != nil {
		t.Fatalf("age message: %v", err)
	}

	candidates, err := store.scrubCandidates(ctx, now-int64(time.Minute/time.Millisecond), nil)
	if err != nil {
		t.Fatalf("scrubCandidates: %v", err)
	}
	if len(candidates) != 1 || !candidates[0].forceScrub {
		t.Fatalf("candidates = %#v, want one permanently filtered row", candidates)
	}

	// CloudKit re-ingest can move the source before the UPDATE. That routing
	// mismatch is recoverable, so the write-time predicate must invalidate the
	// stale force classification and retain plaintext for authoritative re-ingest.
	if _, err := db.Exec(ctx,
		`UPDATE cloud_chat SET portal_id=$1 WHERE login_id=$2 AND cloud_chat_id=$3`,
		"gid:remapped", testSQLLoginID, chatID,
	); err != nil {
		t.Fatalf("remap source: %v", err)
	}
	scrubbed, err := store.scrubBatchIfEligible(
		ctx,
		"bridge",
		now-int64(time.Minute/time.Millisecond),
		map[bridgedDeliveryKey]string{},
		candidates,
	)
	if err != nil {
		t.Fatalf("scrubBatchIfEligible: %v", err)
	}
	if scrubbed != 0 {
		t.Fatalf("scrubbed %d rows, want 0 after source remap", scrubbed)
	}
	var text sql.NullString
	if err := db.QueryRow(ctx,
		`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
		testSQLLoginID, guid,
	).Scan(&text); err != nil {
		t.Fatalf("read message text: %v", err)
	}
	if !text.Valid || text.String != "must remain recoverable" {
		t.Fatalf("message text = %q (valid=%v), want preserved", text.String, text.Valid)
	}
}

func TestWithActiveRestorePortalsSerializesLifecycle(t *testing.T) {
	client := &IMClient{restorePipelines: map[string]bool{"gid:active": true}}
	entered := make(chan []string, 1)
	release := make(chan struct{})
	scrubDone := make(chan struct{})
	go func() {
		_, _ = client.withActiveRestorePortals(func(portals []string) (int64, error) {
			entered <- portals
			<-release
			return 0, nil
		})
		close(scrubDone)
	}()

	portals := <-entered
	if len(portals) != 1 || portals[0] != "gid:active" {
		t.Fatalf("active portals = %v, want [gid:active]", portals)
	}
	attempted := make(chan struct{})
	mutationDone := make(chan struct{})
	go func() {
		close(attempted)
		client.restorePipelinesMu.Lock()
		client.restorePipelines["gid:new"] = true
		client.restorePipelinesMu.Unlock()
		close(mutationDone)
	}()
	<-attempted
	select {
	case <-mutationDone:
		t.Fatal("restore lifecycle mutated while scrub callback held the lock")
	default:
	}

	close(release)
	<-scrubDone
	<-mutationDone
	if !client.restorePipelines["gid:new"] {
		t.Fatal("restore lifecycle mutation did not resume after scrub completed")
	}
}
