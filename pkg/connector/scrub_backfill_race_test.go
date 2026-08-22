package connector

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"go.mau.fi/util/dbutil"
)

// These tests cover the ordering rule between the privacy scrubber and CloudKit
// backfill. Both read the same cloud_message rows, and the scrubber wins ties:
// once a row is body_scrubbed, cloudRowToBackfillMessages drops it. If that
// happens to every deliverable row a portal has, forward backfill converts to
// zero messages and marks the portal done with an empty room — real history
// stranded on disk with nothing in the log to say so.
//
// Two mechanisms are under test: the hold that keeps the scrubbers off a portal
// that has not delivered yet (pendingBackfillGateSQL), and the pieces
// FetchMessages uses to notice and recover when it happens anyway
// (anyScrubbedDeliverable, rescrubClearedRows).

// scrubRaceFixture builds a store with bridgev2's message table alongside it,
// since scrubBridgedBodies only scrubs rows that have a delivered message row.
func scrubRaceFixture(t *testing.T) (context.Context, *dbutil.Database, *cloudBackfillStore) {
	t.Helper()
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS message (
		id TEXT NOT NULL,
		bridge_id TEXT NOT NULL,
		room_receiver TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create message table: %v", err)
	}
	return ctx, db, store
}

func TestRehydrateErrorsDoNotExposePortalID(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	const portalID = "tel:+15550009999"
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	_, err := store.clearBodyScrubForRehydrate(ctx, portalID)
	if err == nil {
		t.Fatal("clearBodyScrubForRehydrate unexpectedly succeeded on a closed database")
	}
	if strings.Contains(err.Error(), portalID) {
		t.Fatalf("rehydrate error exposed portal ID: %q", err)
	}
}

// TestScrubHoldsOffPendingBackfillPortals is the ordering rule: a delivered body
// may be scrubbed, but not while its portal is still waiting for the forward
// backfill that puts the rest of its history in the room.
//
// The negative cases carry as much weight as the positive one. The hold must be
// narrow — it suspends a privacy control — so a portal that already delivered, a
// portal Apple filtered into the junk bucket (which never becomes a portal at
// all, so it would otherwise be exempt forever), and a portal old enough to be
// past the hold window must all still scrub.
func TestScrubHoldsOffPendingBackfillPortals(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	const bridgeID = "test-bridge"
	now := time.Now().UnixMilli()

	type portal struct {
		id         string
		chatID     string
		isFiltered int64
		createdTS  int64
		done       bool
	}
	portals := []portal{
		// Still waiting for its first forward backfill: held.
		{id: "tel:+15550001111", chatID: "C-PENDING", createdTS: now},
		// Forward backfill already delivered: nothing left to protect.
		{id: "tel:+15550002222", chatID: "C-DONE", createdTS: now, done: true},
		// Filtered chat. bridge_filtered_chats is off by default, so these never
		// become portals and never reach fwd_backfill_done — holding them would
		// mean holding them forever.
		{id: "tel:+15550003333", chatID: "C-FILTERED", createdTS: now, isFiltered: 1},
		// Known since long before the hold window. Whatever is wrong with this
		// portal, privacy stops waiting on it.
		{id: "tel:+15550004444", chatID: "C-STALE",
			createdTS: time.Now().Add(-2 * pendingBackfillScrubHold).UnixMilli()},
	}

	for _, p := range portals {
		if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{{
			CloudChatID: p.chatID, PortalID: p.id, Service: "iMessage",
			ParticipantsJSON: "[]", UpdatedTS: now, IsFiltered: p.isFiltered,
		}}); err != nil {
			t.Fatalf("upsertChatBatch %s: %v", p.id, err)
		}
		if _, err := db.Exec(ctx,
			`UPDATE cloud_chat SET created_ts=$3 WHERE login_id=$1 AND cloud_chat_id=$2`,
			testSQLLoginID, p.chatID, p.createdTS,
		); err != nil {
			t.Fatalf("set created_ts %s: %v", p.id, err)
		}
		if p.done {
			store.markForwardBackfillDone(ctx, p.id)
		}

		guid := "GUID-" + p.chatID
		if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
			GUID: guid, CloudChatID: p.chatID, PortalID: p.id,
			TimestampMS: now, Text: "plaintext", Service: "iMessage", HasBody: true,
		}}); err != nil {
			t.Fatalf("upsertMessageBatch %s: %v", p.id, err)
		}
		// Delivered to Matrix — the precondition scrubBridgedBodies requires.
		if _, err := db.Exec(ctx,
			`INSERT INTO message (id, bridge_id, room_receiver) VALUES ($1, $2, $3)`,
			guid, bridgeID, string(testSQLLoginID),
		); err != nil {
			t.Fatalf("insert bridgev2 message row %s: %v", p.id, err)
		}
	}

	// Age every row past the grace window so only the hold is under test.
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$2 WHERE login_id=$1`,
		testSQLLoginID, now-int64(time.Hour/time.Millisecond),
	); err != nil {
		t.Fatalf("age messages: %v", err)
	}

	if _, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, nil); err != nil {
		t.Fatalf("scrubBridgedBodies: %v", err)
	}

	textOf := func(guid string) sql.NullString {
		t.Helper()
		var text sql.NullString
		if err := db.QueryRow(ctx,
			`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
			testSQLLoginID, guid,
		).Scan(&text); err != nil {
			t.Fatalf("read back %s: %v", guid, err)
		}
		return text
	}

	if got := textOf("GUID-C-PENDING"); !got.Valid {
		t.Error("body of a portal still awaiting forward backfill was scrubbed — " +
			"conversion drops scrubbed rows, so this is how a portal ends up marked done with an empty room")
	}
	for _, guid := range []string{"GUID-C-DONE", "GUID-C-FILTERED", "GUID-C-STALE"} {
		if got := textOf(guid); got.Valid {
			t.Errorf("%s still has plaintext %q after scrub — the hold must not exempt this portal", guid, got.String)
		}
	}
}

func TestScrubBridgedBodiesClearsUndeliverableFilteredPlaintext(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	const portalID = "tel:+15550008888"
	now := time.Now().UnixMilli()
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{
		{CloudChatID: "C-UNFILTERED", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: now},
		{CloudChatID: "C-FILTERED", PortalID: portalID, Service: "SMS", ParticipantsJSON: "[]", UpdatedTS: now, IsFiltered: 1},
		{CloudChatID: "C-ALL-UNFILTERED", PortalID: "p-all-unfiltered", Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: now},
	}); err != nil {
		t.Fatalf("upsert chats: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "G-UNFILTERED", CloudChatID: "C-UNFILTERED", PortalID: portalID, TimestampMS: 1000, Text: "still needed", Service: "iMessage", HasBody: true},
		{GUID: "G-FILTERED", CloudChatID: "C-FILTERED", PortalID: portalID, TimestampMS: 2000, Text: "must not remain on disk", Service: "SMS", HasBody: true},
		{GUID: "G-MIXED-EMPTY", CloudChatID: "", PortalID: portalID, TimestampMS: 3000, Text: "ambiguous mixed history", Service: "iMessage", HasBody: true},
		{GUID: "G-MIXED-UNKNOWN", CloudChatID: "unknown-source", PortalID: portalID, TimestampMS: 4000, Text: "unknown mixed history", Service: "iMessage", HasBody: true},
		{GUID: "G-ALL-UNFILTERED-LEGACY", CloudChatID: "legacy-source", PortalID: "p-all-unfiltered", TimestampMS: 5000, Text: "restorable legacy history", Service: "iMessage", HasBody: true},
	}); err != nil {
		t.Fatalf("upsert messages: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE cloud_message SET updated_ts=$2 WHERE login_id=$1`, testSQLLoginID, now-int64(time.Hour/time.Millisecond)); err != nil {
		t.Fatalf("age messages: %v", err)
	}

	scrubbed, err := store.scrubBridgedBodies(ctx, "test-bridge", time.Minute, nil)
	if err != nil {
		t.Fatalf("scrubBridgedBodies: %v", err)
	}
	if scrubbed != 3 {
		t.Fatalf("scrubbed rows = %d, want exact filtered plus two ambiguous mixed-sibling rows", scrubbed)
	}
	for _, tc := range []struct {
		guid      string
		wantText  bool
		wantScrub bool
	}{
		{guid: "G-UNFILTERED", wantText: true, wantScrub: false},
		{guid: "G-FILTERED", wantText: false, wantScrub: true},
		{guid: "G-MIXED-EMPTY", wantText: false, wantScrub: true},
		{guid: "G-MIXED-UNKNOWN", wantText: false, wantScrub: true},
		{guid: "G-ALL-UNFILTERED-LEGACY", wantText: true, wantScrub: false},
	} {
		var text sql.NullString
		var bodyScrubbed bool
		if err := db.QueryRow(ctx, `SELECT text, body_scrubbed FROM cloud_message WHERE login_id=$1 AND guid=$2`, testSQLLoginID, tc.guid).Scan(&text, &bodyScrubbed); err != nil {
			t.Fatalf("read %s: %v", tc.guid, err)
		}
		if text.Valid != tc.wantText || bodyScrubbed != tc.wantScrub {
			t.Errorf("%s text valid=%v scrubbed=%v, want %v/%v", tc.guid, text.Valid, bodyScrubbed, tc.wantText, tc.wantScrub)
		}
	}
}

func TestScrubBridgedBodiesClearsRemappedSourcePlaintext(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	const oldPortal = "tel:+15550007770"
	const newPortal = "tel:+15550007771"
	const guid = "G-REMAPPED-SOURCE"
	now := time.Now().UnixMilli()
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{
		{CloudChatID: "C-REMAPPED", PortalID: newPortal, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: now},
		{CloudChatID: "C-OLD-LIVE", PortalID: oldPortal, Service: "SMS", ParticipantsJSON: "[]", UpdatedTS: now},
	}); err != nil {
		t.Fatalf("upsert chats: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: guid, CloudChatID: "C-REMAPPED", PortalID: oldPortal,
		TimestampMS: 1000, Text: "stale duplicate-room history", Service: "iMessage", HasBody: true,
	}}); err != nil {
		t.Fatalf("upsert message: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$2 WHERE login_id=$1 AND guid=$3`,
		testSQLLoginID, now-int64(time.Hour/time.Millisecond), guid,
	); err != nil {
		t.Fatalf("age message: %v", err)
	}

	scrubbed, err := store.scrubBridgedBodies(ctx, "test-bridge", time.Minute, nil)
	if err != nil {
		t.Fatalf("scrubBridgedBodies: %v", err)
	}
	if scrubbed != 1 {
		t.Fatalf("scrubbed rows = %d, want remapped source scrubbed", scrubbed)
	}
	var text sql.NullString
	var bodyScrubbed bool
	if err := db.QueryRow(ctx,
		`SELECT text, body_scrubbed FROM cloud_message WHERE login_id=$1 AND guid=$2`,
		testSQLLoginID, guid,
	).Scan(&text, &bodyScrubbed); err != nil {
		t.Fatalf("read remapped row: %v", err)
	}
	if text.Valid || !bodyScrubbed {
		t.Fatalf("remapped row text valid=%v scrubbed=%v, want false/true", text.Valid, bodyScrubbed)
	}
}

func TestScrubPendingGateIgnoresStaleDeletedOrFilteredDoneSiblings(t *testing.T) {
	for _, tc := range []struct {
		name        string
		staleChat   string
		markStale   string
		filterStale bool
	}{
		{name: "deleted sibling", staleChat: "C-DELETED-DONE", markStale: "deleted"},
		{name: "filtered sibling", staleChat: "C-FILTERED-DONE", markStale: "filtered", filterStale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, db, store := scrubRaceFixture(t)
			const bridgeID = "test-bridge"
			now := time.Now().UnixMilli()
			portalID := "tel:+15550006666"
			staleFiltered := int64(0)
			if tc.filterStale {
				staleFiltered = 1
			}
			if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{
				{CloudChatID: "C-LIVE-PENDING", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: now},
				{CloudChatID: tc.staleChat, PortalID: portalID, Service: "SMS", ParticipantsJSON: "[]", UpdatedTS: now, IsFiltered: staleFiltered},
			}); err != nil {
				t.Fatalf("upsertChatBatch: %v", err)
			}
			staleUpdate := `UPDATE cloud_chat SET fwd_backfill_done=TRUE`
			if tc.markStale == "deleted" {
				staleUpdate += `, deleted=TRUE`
			}
			staleUpdate += ` WHERE login_id=$1 AND cloud_chat_id=$2`
			if _, err := db.Exec(ctx, staleUpdate, testSQLLoginID, tc.staleChat); err != nil {
				t.Fatalf("mark stale sibling: %v", err)
			}
			if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
				GUID: "GUID-LIVE-PENDING", CloudChatID: "C-LIVE-PENDING", PortalID: portalID,
				TimestampMS: now, Text: "must remain until the live sibling delivers", Service: "iMessage", HasBody: true,
			}}); err != nil {
				t.Fatalf("upsertMessageBatch: %v", err)
			}
			if _, err := db.Exec(ctx,
				`INSERT INTO message (id, bridge_id, room_receiver) VALUES ($1, $2, $3)`,
				"GUID-LIVE-PENDING", bridgeID, string(testSQLLoginID),
			); err != nil {
				t.Fatalf("insert bridge message: %v", err)
			}
			if _, err := db.Exec(ctx,
				`UPDATE cloud_message SET updated_ts=$2 WHERE login_id=$1`,
				testSQLLoginID, now-int64(time.Hour/time.Millisecond),
			); err != nil {
				t.Fatalf("age message: %v", err)
			}

			scrubbed, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, nil)
			if err != nil {
				t.Fatalf("scrubBridgedBodies: %v", err)
			}
			if scrubbed != 0 {
				t.Fatalf("scrubBridgedBodies scrubbed %d rows, want pending live sibling held", scrubbed)
			}
			var text sql.NullString
			if err := db.QueryRow(ctx,
				`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
				testSQLLoginID, "GUID-LIVE-PENDING").Scan(&text); err != nil {
				t.Fatalf("read message text: %v", err)
			}
			if !text.Valid {
				t.Fatalf("pending live sibling body was scrubbed despite stale %s sibling", tc.markStale)
			}
		})
	}
}

func TestScrubPendingGateCorrelatesLiveSiblingCompletion(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	const bridgeID = "test-bridge"
	portalID := "tel:+15550008888"
	now := time.Now().UnixMilli()
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{
		{CloudChatID: "C-LIVE-PENDING", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: now},
		{CloudChatID: "C-LIVE-DONE", PortalID: portalID, Service: "SMS", ParticipantsJSON: "[]", UpdatedTS: now},
	}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_chat SET fwd_backfill_done=TRUE WHERE login_id=$1 AND cloud_chat_id=$2`,
		testSQLLoginID, "C-LIVE-DONE",
	); err != nil {
		t.Fatalf("mark done sibling: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "GUID-LIVE-PENDING", CloudChatID: "C-LIVE-PENDING", PortalID: portalID, TimestampMS: now, Text: "pending sibling", Service: "iMessage", HasBody: true},
		{GUID: "GUID-LIVE-DONE", CloudChatID: "C-LIVE-DONE", PortalID: portalID, TimestampMS: now + 1, Text: "done sibling", Service: "SMS", HasBody: true},
	}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}
	for _, guid := range []string{"GUID-LIVE-PENDING", "GUID-LIVE-DONE"} {
		if _, err := db.Exec(ctx,
			`INSERT INTO message (id, bridge_id, room_receiver) VALUES ($1, $2, $3)`,
			guid, bridgeID, string(testSQLLoginID),
		); err != nil {
			t.Fatalf("insert bridge message %s: %v", guid, err)
		}
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$2 WHERE login_id=$1`,
		testSQLLoginID, now-int64(time.Hour/time.Millisecond),
	); err != nil {
		t.Fatalf("age messages: %v", err)
	}

	scrubbed, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, nil)
	if err != nil {
		t.Fatalf("scrubBridgedBodies: %v", err)
	}
	if scrubbed != 1 {
		t.Fatalf("scrubBridgedBodies scrubbed %d rows, want only the completed sibling", scrubbed)
	}
	var pendingText, doneText sql.NullString
	if err := db.QueryRow(ctx,
		`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
		testSQLLoginID, "GUID-LIVE-PENDING").Scan(&pendingText); err != nil {
		t.Fatalf("read pending sibling: %v", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
		testSQLLoginID, "GUID-LIVE-DONE").Scan(&doneText); err != nil {
		t.Fatalf("read done sibling: %v", err)
	}
	if !pendingText.Valid {
		t.Error("pending live sibling was scrubbed by another live done sibling")
	}
	if doneText.Valid {
		t.Errorf("completed live sibling retained plaintext %q", doneText.String)
	}
}

// TestScrubBridgedBodiesStillScrubsDeletedRowsWhilePending pins the one branch
// the hold deliberately does not cover. A row the user deleted or unsent must
// lose its plaintext immediately: there is no backfill left to protect (every
// backfill reader filters deleted=FALSE), and delaying it would keep content the
// user explicitly removed readable on disk for up to a day.
func TestScrubBridgedBodiesStillScrubsDeletedRowsWhilePending(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	const bridgeID = "test-bridge"
	now := time.Now().UnixMilli()

	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{{
		CloudChatID: "C-PENDING", PortalID: "tel:+15550001111", Service: "iMessage",
		ParticipantsJSON: "[]", UpdatedTS: now,
	}}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: "GUID-DELETED", CloudChatID: "C-PENDING", PortalID: "tel:+15550001111",
		TimestampMS: now, Text: "unsent", Service: "iMessage", HasBody: true, Deleted: true,
	}}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$2 WHERE login_id=$1`,
		testSQLLoginID, now-int64(time.Hour/time.Millisecond),
	); err != nil {
		t.Fatalf("age messages: %v", err)
	}

	scrubbed, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, nil)
	if err != nil {
		t.Fatalf("scrubBridgedBodies: %v", err)
	}
	if scrubbed != 1 {
		t.Fatalf("scrubBridgedBodies scrubbed %d deleted rows, want 1 — the hold must not delay deleted content", scrubbed)
	}
}

func TestScrubHoldsOffOptedInFilteredBackfillPortals(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	store.bridgeFiltered = true
	const bridgeID = "test-bridge"
	now := time.Now().UnixMilli()
	portalID := "tel:+15550005555"
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{{
		CloudChatID: "C-FILTERED-OPTED-IN", PortalID: portalID, Service: "iMessage",
		ParticipantsJSON: "[]", UpdatedTS: now, IsFiltered: 1,
	}}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: "GUID-FILTERED-OPTED-IN", CloudChatID: "C-FILTERED-OPTED-IN", PortalID: portalID,
		TimestampMS: now, Text: "plaintext", Service: "iMessage", HasBody: true,
	}}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO message (id, bridge_id, room_receiver) VALUES ($1, $2, $3)`,
		"GUID-FILTERED-OPTED-IN", bridgeID, string(testSQLLoginID),
	); err != nil {
		t.Fatalf("insert bridgev2 message row: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$2 WHERE login_id=$1`,
		testSQLLoginID, now-int64(time.Hour/time.Millisecond),
	); err != nil {
		t.Fatalf("age message: %v", err)
	}

	if _, err := store.scrubBridgedBodies(ctx, bridgeID, time.Minute, nil); err != nil {
		t.Fatalf("scrubBridgedBodies: %v", err)
	}
	var text sql.NullString
	if err := db.QueryRow(ctx,
		`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
		testSQLLoginID, "GUID-FILTERED-OPTED-IN").Scan(&text); err != nil {
		t.Fatalf("read message text: %v", err)
	}
	if !text.Valid {
		t.Error("body of opted-in filtered portal was scrubbed while its forward backfill was pending")
	}
}

// TestScrubUnbridgedTailHoldsOffPendingBackfillPortals covers the same hold on
// the tail scrubber, which is the more dangerous of the two: it clears rows that
// were never delivered, choosing them by position rather than by delivery. Its
// threshold counts every non-deleted row while listLatestMessages returns only
// contentful ones, so before delivery the "unreachable" tail can still contain
// rows forward backfill was going to send.
func TestScrubUnbridgedTailHoldsOffPendingBackfillPortals(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	now := time.Now().UnixMilli()

	for _, p := range []struct {
		id, chatID string
		done       bool
	}{
		{id: "tel:+15550001111", chatID: "C-PENDING"},
		{id: "tel:+15550002222", chatID: "C-DONE", done: true},
	} {
		if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{{
			CloudChatID: p.chatID, PortalID: p.id, Service: "iMessage",
			ParticipantsJSON: "[]", UpdatedTS: now,
		}}); err != nil {
			t.Fatalf("upsertChatBatch %s: %v", p.id, err)
		}
		if p.done {
			store.markForwardBackfillDone(ctx, p.id)
		}
		// Four rows each, keeping two: rows 1 and 2 are the tail.
		for i := 1; i <= 4; i++ {
			guid := p.chatID + "-" + string(rune('0'+i))
			if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
				GUID: guid, RecordName: "rec-" + guid, CloudChatID: p.chatID, PortalID: p.id,
				TimestampMS: now + int64(i), Text: "plaintext", Service: "iMessage", HasBody: true,
			}}); err != nil {
				t.Fatalf("upsertMessageBatch %s: %v", guid, err)
			}
		}
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$2 WHERE login_id=$1`,
		testSQLLoginID, now-int64(time.Hour/time.Millisecond),
	); err != nil {
		t.Fatalf("age messages: %v", err)
	}

	scrubbed, err := store.scrubUnbridgedTail(ctx, 2, time.Minute, nil)
	if err != nil {
		t.Fatalf("scrubUnbridgedTail: %v", err)
	}
	if scrubbed != 2 {
		t.Errorf("scrubUnbridgedTail scrubbed %d rows, want 2 (the delivered portal's tail only)", scrubbed)
	}
	var pendingPlaintext int
	if err := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM cloud_message WHERE login_id=$1 AND portal_id=$2 AND COALESCE(text,'') <> ''`,
		testSQLLoginID, "tel:+15550001111",
	).Scan(&pendingPlaintext); err != nil {
		t.Fatalf("count pending portal rows: %v", err)
	}
	if pendingPlaintext != 4 {
		t.Errorf("portal awaiting forward backfill kept %d/4 rows with text — its tail was scrubbed before delivery", pendingPlaintext)
	}
}

func TestScrubUnbridgedTailCountsOnlyEligibleMixedSiblingRows(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	const portalID = "tel:+15550007777"
	now := time.Now().UnixMilli()
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{
		{CloudChatID: "C-UNFILTERED", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: now},
		{CloudChatID: "C-FILTERED", PortalID: portalID, Service: "SMS", ParticipantsJSON: "[]", UpdatedTS: now, IsFiltered: 1},
	}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}
	store.markForwardBackfillDone(ctx, portalID)
	rows := []cloudMessageRow{
		{GUID: "U-1", RecordName: "rec-U-1", CloudChatID: "C-UNFILTERED", PortalID: portalID, TimestampMS: 1000, Text: "unfiltered one", Service: "iMessage", HasBody: true},
		{GUID: "U-2", RecordName: "rec-U-2", CloudChatID: "C-UNFILTERED", PortalID: portalID, TimestampMS: 2000, Text: "unfiltered two", Service: "iMessage", HasBody: true},
		{GUID: "U-3", RecordName: "rec-U-3", CloudChatID: "C-UNFILTERED", PortalID: portalID, TimestampMS: 3000, Text: "unfiltered three", Service: "iMessage", HasBody: true},
		{GUID: "F-1", RecordName: "rec-F-1", CloudChatID: "C-FILTERED", PortalID: portalID, TimestampMS: 4000, Text: "filtered one", Service: "SMS", HasBody: true},
		{GUID: "F-2", RecordName: "rec-F-2", CloudChatID: "C-FILTERED", PortalID: portalID, TimestampMS: 5000, Text: "filtered two", Service: "SMS", HasBody: true},
		{GUID: "F-3", RecordName: "rec-F-3", CloudChatID: "C-FILTERED", PortalID: portalID, TimestampMS: 6000, Text: "filtered three", Service: "SMS", HasBody: true},
	}
	if err := store.upsertMessageBatch(ctx, rows); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}
	if _, err := db.Exec(ctx,
		`UPDATE cloud_message SET updated_ts=$2 WHERE login_id=$1`,
		testSQLLoginID, now-int64(time.Hour/time.Millisecond),
	); err != nil {
		t.Fatalf("age messages: %v", err)
	}

	scrubbed, err := store.scrubUnbridgedTail(ctx, 2, time.Minute, nil)
	if err != nil {
		t.Fatalf("scrubUnbridgedTail: %v", err)
	}
	if scrubbed != 1 {
		t.Fatalf("scrubUnbridgedTail scrubbed %d rows, want only the oldest eligible unfiltered row", scrubbed)
	}
	for _, tc := range []struct {
		guid string
		want bool
	}{
		{guid: "U-1", want: false},
		{guid: "U-2", want: true},
		{guid: "U-3", want: true},
		{guid: "F-1", want: true},
		{guid: "F-2", want: true},
		{guid: "F-3", want: true},
	} {
		var text sql.NullString
		if err := db.QueryRow(ctx,
			`SELECT text FROM cloud_message WHERE login_id=$1 AND guid=$2`,
			testSQLLoginID, tc.guid).Scan(&text); err != nil {
			t.Fatalf("read %s: %v", tc.guid, err)
		}
		if text.Valid != tc.want {
			t.Errorf("%s text validity = %v, want %v", tc.guid, text.Valid, tc.want)
		}
	}
}

// TestAnyScrubbedDeliverable covers the in-memory check FetchMessages uses to
// decide whether an empty conversion is worth a CloudKit round trip. It runs on
// rows already in hand precisely so the empty path adds no query to a pool
// clamped at one connection, so it has to be right about which rows conversion
// actually drops for being scrubbed.
func TestAnyScrubbedDeliverable(t *testing.T) {
	reaction := uint32(2000)
	regular := uint32(0)

	for _, tc := range []struct {
		name string
		rows []cloudMessageRow
		want bool
	}{
		{name: "no rows"},
		{
			name: "live rows only",
			rows: []cloudMessageRow{{GUID: "A", Text: "hi"}, {GUID: "B", Text: "there"}},
		},
		{
			// Scrubbed reactions still render (cloudTapbackToBackfill reads
			// tapback_type, which the scrubber preserves), so they are not a
			// reason to go back to CloudKit.
			name: "scrubbed reaction only",
			rows: []cloudMessageRow{{GUID: "A", BodyScrubbed: true, TapbackType: &reaction}},
		},
		{
			name: "scrubbed message among live rows",
			rows: []cloudMessageRow{{GUID: "A", Text: "hi"}, {GUID: "B", BodyScrubbed: true}},
			want: true,
		},
		{
			// tapback_type 0 is a regular message, not a reaction — the
			// NULL-vs-0 distinction isCloudReactionRow exists for.
			name: "scrubbed message with tapback_type 0",
			rows: []cloudMessageRow{{GUID: "A", BodyScrubbed: true, TapbackType: &regular}},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyScrubbedDeliverable(tc.rows); got != tc.want {
				t.Errorf("anyScrubbedDeliverable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRescrubClearedRowsDoesNotCaptureNewAPNSStub(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	now := time.Now().UnixMilli()

	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "G-CLEARED", PortalID: "p1", CloudChatID: "C1", TimestampMS: now, Service: "iMessage", HasBody: true},
	}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE cloud_message SET body_scrubbed=TRUE WHERE login_id=$1 AND guid='G-CLEARED'`, testSQLLoginID); err != nil {
		t.Fatalf("mark original row scrubbed: %v", err)
	}
	var beforeClear bool
	if err := db.QueryRow(ctx, `SELECT body_scrubbed FROM cloud_message WHERE login_id=$1 AND guid='G-CLEARED'`, testSQLLoginID).Scan(&beforeClear); err != nil {
		t.Fatalf("read original scrub state before clear: %v", err)
	} else if !beforeClear {
		t.Fatal("original row was not marked scrubbed before clear")
	}
	clearedAttempt, err := store.clearBodyScrubForRehydrate(ctx, "p1")
	if err != nil {
		t.Fatalf("clearBodyScrubForRehydrate: %v", err)
	}
	if len(clearedAttempt.Rows) != 1 || clearedAttempt.Rows[0].GUID != "G-CLEARED" {
		t.Fatalf("cleared rows = %v, want G-CLEARED", clearedAttempt.Rows)
	}

	// This is the APNs/CloudKit interleaving that the former updated_ts window
	// captured: the placeholder did not exist when the scrub flags were cleared.
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "G-NEW-APNS", PortalID: "p1", CloudChatID: "C1", TimestampMS: now + 1, Service: "iMessage", HasBody: true},
	}); err != nil {
		t.Fatalf("insert concurrent APNs stub: %v", err)
	}
	var clearedState bool
	var clearedText, clearedSubject string
	if err := db.QueryRow(ctx, `SELECT body_scrubbed, COALESCE(text, ''), COALESCE(subject, '') FROM cloud_message WHERE login_id=$1 AND guid='G-CLEARED'`, testSQLLoginID).Scan(&clearedState, &clearedText, &clearedSubject); err != nil {
		t.Fatalf("read cleared row before re-arm: %v", err)
	}
	if clearedState || clearedText != "" || clearedSubject != "" {
		t.Fatalf("cleared row before re-arm = scrubbed %v text %q subject %q", clearedState, clearedText, clearedSubject)
	}
	if n, err := store.rescrubClearedRows(ctx, "p1", clearedAttempt); err != nil {
		t.Fatalf("rescrubClearedRows: %v", err)
	} else if n != 1 {
		t.Fatalf("rescrubbed rows = %d, want 1", n)
	}

	var originalScrubbed, stubScrubbed bool
	if err := db.QueryRow(ctx, `SELECT body_scrubbed FROM cloud_message WHERE login_id=$1 AND guid='G-CLEARED'`, testSQLLoginID).Scan(&originalScrubbed); err != nil {
		t.Fatalf("read original scrub state: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT body_scrubbed FROM cloud_message WHERE login_id=$1 AND guid='G-NEW-APNS'`, testSQLLoginID).Scan(&stubScrubbed); err != nil {
		t.Fatalf("read stub scrub state: %v", err)
	}
	if !originalScrubbed || stubScrubbed {
		t.Fatalf("scrub states original=%v stub=%v, want true false", originalScrubbed, stubScrubbed)
	}

	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "G-NEW-APNS", PortalID: "p1", CloudChatID: "C1", TimestampMS: now + 1, Text: "arrived later", Service: "iMessage", HasBody: true},
	}); err != nil {
		t.Fatalf("fill concurrent APNs stub: %v", err)
	}
	var text string
	if err := db.QueryRow(ctx, `SELECT text FROM cloud_message WHERE login_id=$1 AND guid='G-NEW-APNS'`, testSQLLoginID).Scan(&text); err != nil {
		t.Fatalf("read filled stub: %v", err)
	}
	if text != "arrived later" {
		t.Fatalf("filled stub text = %q, want body accepted", text)
	}
}

func TestRehydrateClearExcludesFilteredSibling(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	const portalID = "p1"
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{
		{CloudChatID: "C-UNFILTERED", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: 1000},
		{CloudChatID: "C-FILTERED", PortalID: portalID, Service: "SMS", ParticipantsJSON: "[]", UpdatedTS: 1000, IsFiltered: 1},
	}); err != nil {
		t.Fatalf("upsert chats: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "G-UNFILTERED", CloudChatID: "C-UNFILTERED", PortalID: portalID, TimestampMS: 1000, Text: "visible", Service: "iMessage", HasBody: true},
		{GUID: "G-FILTERED", CloudChatID: "C-FILTERED", PortalID: portalID, TimestampMS: 1000, Text: "hidden", Service: "SMS", HasBody: true},
	}); err != nil {
		t.Fatalf("upsert messages: %v", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE cloud_message
		SET body_scrubbed=TRUE, text=NULL, subject=NULL, sender=''
		WHERE login_id=$1 AND portal_id=$2
	`, testSQLLoginID, portalID); err != nil {
		t.Fatalf("scrub messages: %v", err)
	}

	attempt, err := store.clearBodyScrubForRehydrate(ctx, portalID)
	if err != nil {
		t.Fatalf("clearBodyScrubForRehydrate: %v", err)
	}
	if len(attempt.Rows) != 1 || attempt.Rows[0].GUID != "G-UNFILTERED" {
		t.Fatalf("rehydrate cleared rows = %#v, want only G-UNFILTERED", attempt.Rows)
	}

	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "G-UNFILTERED", CloudChatID: "C-UNFILTERED", PortalID: portalID, TimestampMS: 1000, Text: "visible restored", Service: "iMessage", HasBody: true},
		{GUID: "G-FILTERED", CloudChatID: "C-FILTERED", PortalID: portalID, TimestampMS: 1000, Text: "hidden restored", Service: "SMS", HasBody: true},
	}); err != nil {
		t.Fatalf("rehydrate upsert: %v", err)
	}

	var unfilteredScrubbed, filteredScrubbed bool
	var unfilteredText, filteredText sql.NullString
	if err := db.QueryRow(ctx, `
		SELECT body_scrubbed, text FROM cloud_message
		WHERE login_id=$1 AND guid='G-UNFILTERED'
	`, testSQLLoginID).Scan(&unfilteredScrubbed, &unfilteredText); err != nil {
		t.Fatalf("read unfiltered row: %v", err)
	}
	if err := db.QueryRow(ctx, `
		SELECT body_scrubbed, text FROM cloud_message
		WHERE login_id=$1 AND guid='G-FILTERED'
	`, testSQLLoginID).Scan(&filteredScrubbed, &filteredText); err != nil {
		t.Fatalf("read filtered row: %v", err)
	}
	if unfilteredScrubbed || !unfilteredText.Valid || unfilteredText.String != "visible restored" {
		t.Fatalf("unfiltered row = scrubbed %v text %#v, want restored plaintext", unfilteredScrubbed, unfilteredText)
	}
	if !filteredScrubbed || filteredText.Valid {
		t.Fatalf("filtered row = scrubbed %v text %#v, want scrubbed and NULL", filteredScrubbed, filteredText)
	}
}

func TestRehydrateClearNeverReopensDeletedBodies(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	now := time.Now().UnixMilli()
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: "G-DELETED", PortalID: "p1", CloudChatID: "C1", TimestampMS: now,
		Text: "deleted body", Service: "iMessage", HasBody: true, Deleted: true,
	}}); err != nil {
		t.Fatalf("upsert deleted message: %v", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE cloud_message SET body_scrubbed=TRUE, text=NULL, subject=NULL, sender=''
		WHERE login_id=$1 AND guid='G-DELETED'
	`, testSQLLoginID); err != nil {
		t.Fatalf("scrub deleted message: %v", err)
	}

	attempt, err := store.clearBodyScrubForRehydrate(ctx, "p1")
	if err != nil {
		t.Fatalf("clearBodyScrubForRehydrate: %v", err)
	}
	if len(attempt.Rows) != 0 {
		t.Fatalf("rehydrate captured deleted rows: %#v", attempt.Rows)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: "G-DELETED", PortalID: "p1", CloudChatID: "C1", TimestampMS: now,
		Text: "must stay deleted", Service: "iMessage", HasBody: true, Deleted: true,
	}}); err != nil {
		t.Fatalf("simulate CloudKit rehydrate upsert: %v", err)
	}
	if n, err := store.rescrubClearedRows(ctx, "p1", attempt); err != nil {
		t.Fatalf("rescrubClearedRows: %v", err)
	} else if n != 0 {
		t.Fatalf("rescrubbed deleted rows = %d, want 0 because none were reopened", n)
	}

	var bodyScrubbed bool
	var text sql.NullString
	if err := db.QueryRow(ctx, `
		SELECT body_scrubbed, text FROM cloud_message
		WHERE login_id=$1 AND guid='G-DELETED'
	`, testSQLLoginID).Scan(&bodyScrubbed, &text); err != nil {
		t.Fatalf("read deleted row: %v", err)
	}
	if !bodyScrubbed || text.Valid {
		t.Fatalf("deleted row = scrubbed %v text %#v, want true and NULL", bodyScrubbed, text)
	}
}

func TestRescrubClearedRowsRearmsMissedAttachmentOnlyRow(t *testing.T) {
	ctx, db, store := scrubRaceFixture(t)
	now := time.Now().UnixMilli()
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "G-FETCHED", PortalID: "p1", CloudChatID: "C1", TimestampMS: now, AttachmentsJSON: `[{"guid":"A1"}]`, Service: "iMessage", HasBody: true},
		{GUID: "g-fetched", PortalID: "p1", CloudChatID: "C1", TimestampMS: now, AttachmentsJSON: `[{"guid":"A1-case-variant"}]`, Service: "iMessage", HasBody: true},
		{GUID: "G-MISSED", PortalID: "p1", CloudChatID: "C1", TimestampMS: now, AttachmentsJSON: `[{"guid":"A2"}]`, Service: "iMessage", HasBody: true},
	}); err != nil {
		t.Fatalf("upsert attachment rows: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE cloud_message SET body_scrubbed=TRUE WHERE login_id=$1 AND portal_id='p1'`, testSQLLoginID); err != nil {
		t.Fatalf("mark attachment rows scrubbed: %v", err)
	}
	clearedAttempt, err := store.clearBodyScrubForRehydrate(ctx, "p1")
	if err != nil {
		t.Fatalf("clearBodyScrubForRehydrate: %v", err)
	}
	// Simulate any CloudKit writer restoring one attachment-only row while the
	// automatic targeted fetch is in flight. updated_ts must prove this exact
	// row was refreshed even though its retained attachment metadata is unchanged.
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "G-FETCHED", PortalID: "p1", CloudChatID: "C1", TimestampMS: now, AttachmentsJSON: `[{"guid":"A1"}]`, Service: "iMessage", HasBody: true},
	}); err != nil {
		t.Fatalf("simulate concurrent attachment restore: %v", err)
	}
	if n, err := store.rescrubClearedRows(ctx, "p1", clearedAttempt); err != nil {
		t.Fatalf("rescrubClearedRows: %v", err)
	} else if n != 2 {
		t.Fatalf("rescrubbed rows = %d, want 2", n)
	}

	for guid, want := range map[string]bool{"G-FETCHED": false, "g-fetched": true, "G-MISSED": true} {
		var got bool
		if err := db.QueryRow(ctx, `SELECT body_scrubbed FROM cloud_message WHERE login_id=$1 AND guid=$2`, testSQLLoginID, guid).Scan(&got); err != nil {
			t.Fatalf("read %s scrub state: %v", guid, err)
		}
		if got != want {
			t.Errorf("%s body_scrubbed = %v, want %v", guid, got, want)
		}
	}
}
