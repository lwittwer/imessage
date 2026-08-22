package connector

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestStrandedBackfillReconciliationQueryUsesPostgresBoolean(t *testing.T) {
	if !strings.Contains(strandedBackfillReconciliationQuery, "bt.is_done=TRUE") {
		t.Fatalf("reconciliation query does not use a portable boolean literal: %s", strandedBackfillReconciliationQuery)
	}
	if strings.Contains(strandedBackfillReconciliationQuery, "bt.is_done=1") {
		t.Fatalf("reconciliation query still compares a boolean column to integer 1: %s", strandedBackfillReconciliationQuery)
	}
}

func TestSetSyncStateErrorStoresSafeClassification(t *testing.T) {
	ctx := context.Background()
	rawDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	db, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := newCloudBackfillStore(db, networkid.UserLoginID("login"))
	if err = store.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	const rawError = "CloudKit request https://example.invalid/records?token=secret failed for record-name"
	if err = store.setSyncStateError(ctx, cloudZoneChats, rawError); err != nil {
		t.Fatal(err)
	}
	var got string
	if err = db.QueryRow(ctx, `SELECT last_error FROM cloud_sync_state WHERE login_id=$1 AND zone=$2`, store.loginID, cloudZoneChats).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == rawError || got != "cloudkit_sync_failed" {
		t.Fatalf("setSyncStateError persisted %q, want safe classification", got)
	}
}

func TestListPortalIDsWithNewestTimestampIncludesChatOnlyPortals(t *testing.T) {
	ctx := context.Background()
	rawDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	db, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := newCloudBackfillStore(db, networkid.UserLoginID("login"))
	if err = store.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	now := int64(1000)
	if _, err = db.Exec(ctx, `
		INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, display_name, created_ts, updated_ts, deleted, is_filtered)
		VALUES
			($1, 'chat-only', 'tel:+15550000001', NULL, $2, $2, FALSE, 0),
			($1, 'with-message', 'tel:+15550000002', NULL, $2, $2, FALSE, 0),
			($1, 'reaction-only', 'tel:+15550000003', NULL, $2, $2, FALSE, 0),
			($1, 'scrubbed', 'tel:+15550000004', NULL, $2, $2, FALSE, 0),
			($1, 'senderless', 'tel:+15550000005', NULL, $2, $2, FALSE, 0),
			($1, 'senderless-group', 'gid:senderless', NULL, $2, $2, FALSE, 0),
			($1, 'rename', 'gid:rename', 'Renamed Group', $2, $2, FALSE, 0),
			($1, 'rename-trimmed', 'gid:rename-trimmed', 'Renamed Trim', $2, $2, FALSE, 0),
			($1, 'rename-padded-display', 'gid:rename-padded-display', 'Renamed Padded ' || char(65532), $2, $2, FALSE, 0),
			($1, 'unicode-whitespace', 'tel:+15550000007', NULL, $2, $2, FALSE, 0),
			($1, 'filtered', 'tel:+15550000006', NULL, $2, $2, FALSE, 1)
	`, store.loginID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `
		INSERT INTO cloud_message (
			login_id, guid, portal_id, timestamp_ms, sender, is_from_me, text, record_name,
			tapback_type, tapback_target_guid, attachments_json, has_body, body_scrubbed, created_ts, updated_ts
		)
		VALUES
			($1, 'msg-1', 'tel:+15550000002', 2000, 'tel:+15551111111', FALSE, 'hello', 'record-1', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'whitespace-1', 'tel:+15550000002', 8000, 'tel:+15551111111', FALSE, '  ' || char(10), 'record-1b', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'subject-whitespace-1', 'tel:+15550000002', 9000, 'tel:+15551111111', FALSE, '', 'record-1c', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'reaction-1', 'tel:+15550000003', 3000, 'tel:+15551111111', FALSE, '', 'record-2', 2000, 'msg-1', '', TRUE, FALSE, $2, $2),
			($1, 'scrubbed-1', 'tel:+15550000004', 4000, 'tel:+15551111111', FALSE, '', 'record-3', NULL, NULL, '', TRUE, TRUE, $2, $2),
			($1, 'senderless-1', 'tel:+15550000005', 5000, '', FALSE, 'senderless', 'record-4', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'senderless-group-1', 'gid:senderless', 5500, '', FALSE, 'senderless group', 'record-4b', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'rename-1', 'gid:rename', 6000, 'tel:+15551111111', FALSE, 'Renamed Group', 'record-5', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'rename-trimmed-1', 'gid:rename-trimmed', 6500, 'tel:+15551111111', FALSE, 'Renamed Trim' || char(10), 'record-5b', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'rename-padded-display-1', 'gid:rename-padded-display', 6600, 'tel:+15551111111', FALSE, 'Renamed Padded', 'record-5d', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'unicode-whitespace-1', 'tel:+15550000007', 6750, 'tel:+15551111111', FALSE, char(160), 'record-5c', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'filtered-1', 'tel:+15550000006', 7000, 'tel:+15551111111', FALSE, 'filtered', 'record-6', NULL, NULL, '', TRUE, FALSE, $2, $2)
	`, store.loginID, now); err != nil {
		t.Fatal(err)
	}
	// Message filtering now fails closed when a portal has live cloud_chat
	// metadata but the row has no source chat_id. Populate this legacy fixture's
	// rows with their exact cloud_chat sibling so the test exercises content
	// eligibility rather than the missing-source safety rule.
	if _, err = db.Exec(ctx, `
		UPDATE cloud_message
		SET chat_id = (
			SELECT cc.cloud_chat_id
			FROM cloud_chat cc
			WHERE cc.login_id=cloud_message.login_id
			  AND cc.portal_id=cloud_message.portal_id
			ORDER BY cc.cloud_chat_id
			LIMIT 1
		)
		WHERE login_id=$1 AND COALESCE(chat_id, '') = ''
	`, store.loginID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `
		UPDATE cloud_message
		SET subject = ' ' || char(10)
		WHERE login_id=$1 AND guid='subject-whitespace-1'
	`, store.loginID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `
		UPDATE cloud_chat
		SET updated_ts=10000
		WHERE login_id=$1 AND portal_id='tel:+15550000002'
	`, store.loginID); err != nil {
		t.Fatal(err)
	}

	got, err := store.listPortalIDsWithNewestTimestamp(ctx, 1<<31-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d portals (%#v), want readable-message plus metadata-only portals", len(got), got)
	}
	if got[0].PortalID != "tel:+15550000002" || got[0].ActivityTS != 10000 || got[0].NewestTS != 2000 || got[0].MessageCount != 1 || got[0].ContentfulCount != 1 {
		t.Fatalf("got first portal %#v, want portal ordered by newer chat metadata without advancing message timestamp", got[0])
	}
	if got[0].MessageActivityTS != 2000 || got[0].MetadataTS != 10000 {
		t.Fatalf("got first portal split activity %#v, want message=2000 metadata=10000", got[0])
	}
	if got[1].PortalID != "tel:+15550000005" || got[1].ActivityTS != 5000 || got[1].NewestTS != 5000 || got[1].MessageCount != 1 || got[1].ContentfulCount != 1 {
		t.Fatalf("got second portal %#v, want senderless DM fallback message", got[1])
	}
	if got[1].MessageActivityTS != 5000 || got[1].MetadataTS != now {
		t.Fatalf("got second portal split activity %#v, want message=5000 metadata=%d", got[1], now)
	}
	if got[2].PortalID != "tel:+15550000003" || got[2].ActivityTS != 3000 || got[2].NewestTS != 0 || got[2].MessageCount != 1 || got[2].ContentfulCount != 0 {
		t.Fatalf("got third portal %#v, want reaction-only readable candidate with no contentful messages", got[2])
	}
	if got[2].MessageActivityTS != 3000 || got[2].MetadataTS != now {
		t.Fatalf("got third portal split activity %#v, want message=3000 metadata=%d", got[2], now)
	}
	byPortal := make(map[string]portalWithNewestMessage, len(got))
	for _, p := range got {
		byPortal[p.PortalID] = p
	}
	for _, portalID := range []string{
		"tel:+15550000001",
		"tel:+15550000004",
		"gid:senderless",
		"gid:rename",
		"gid:rename-trimmed",
		"gid:rename-padded-display",
		"tel:+15550000007",
	} {
		p, ok := byPortal[portalID]
		if !ok {
			t.Fatalf("metadata-only portal %q missing from candidates: %#v", portalID, got)
		}
		if p.ActivityTS != now || p.MessageActivityTS != 0 || p.MetadataTS != now || p.NewestTS != 0 || p.MessageCount != 0 || p.ContentfulCount != 0 {
			t.Fatalf("metadata-only portal %q = %#v, want chat timestamp with no message/contentful count", portalID, p)
		}
	}
	if _, ok := byPortal["tel:+15550000006"]; ok {
		t.Fatalf("filtered portal was included: %#v", got)
	}
	count, err := store.countBackfillableMessages(ctx, "tel:+15550000002", true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("countBackfillableMessages(contentful) = %d, want 1", count)
	}
	newest, err := store.getNewestBackfillableMessageTimestamp(ctx, "tel:+15550000002", true)
	if err != nil {
		t.Fatal(err)
	}
	if newest != 2000 {
		t.Fatalf("getNewestBackfillableMessageTimestamp(contentful) = %d, want 2000", newest)
	}
	for _, portalID := range []string{"tel:+15550000001", "tel:+15550000003", "tel:+15550000004", "gid:senderless", "gid:rename", "gid:rename-trimmed", "gid:rename-padded-display", "tel:+15550000007", "tel:+15550000006"} {
		hasMessages, err := store.hasContentfulMessages(ctx, portalID)
		if err != nil {
			t.Fatal(err)
		}
		if hasMessages {
			t.Fatalf("hasContentfulMessages(%q) = true, want false", portalID)
		}
	}
}

func TestListPortalIDsWithNewestTimestampBridgesFilteredWhenOptedIn(t *testing.T) {
	ctx := context.Background()
	rawDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	db, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defaultStore := newCloudBackfillStore(db, networkid.UserLoginID("login"))
	if err = defaultStore.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	const (
		portalID = "tel:+15550000099"
		now      = int64(1000)
	)
	if _, err = db.Exec(ctx, `
		INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, created_ts, updated_ts, deleted, is_filtered)
		VALUES ($1, 'filtered-chat', $2, $3, $3, FALSE, 1)
	`, defaultStore.loginID, portalID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `
		INSERT INTO cloud_message (
			login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me, text, record_name,
			has_body, body_scrubbed, created_ts, updated_ts
		) VALUES ($1, 'filtered-message', 'filtered-chat', $2, 2000, 'tel:+15551111111', FALSE, 'hello', 'record-filtered', TRUE, FALSE, $3, $3)
	`, defaultStore.loginID, portalID, now); err != nil {
		t.Fatal(err)
	}

	if got, err := defaultStore.listPortalIDsWithNewestTimestamp(ctx, 1<<31-1); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Fatalf("default filtered-chat candidates = %#v, want none", got)
	}

	optInStore := newCloudBackfillStore(db, networkid.UserLoginID("login"), true)
	got, err := optInStore.listPortalIDsWithNewestTimestamp(ctx, 1<<31-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PortalID != portalID || got[0].ContentfulCount != 1 {
		t.Fatalf("opted-in filtered-chat candidates = %#v, want one contentful portal", got)
	}
}

func TestListPortalIDsWithNewestTimestampIncludesFilteredMetadataWhenOptedIn(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	defaultStore := newCloudBackfillStore(db, testSQLLoginID)
	if err := defaultStore.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	const (
		portalID = "gid:filtered-metadata"
		now      = int64(2000)
	)
	if err := defaultStore.upsertChatBatch(ctx, []cloudChatUpsertRow{{
		CloudChatID: "filtered-metadata-chat", PortalID: portalID, Service: "iMessage",
		DisplayName: "Updated Group", ParticipantsJSON: "[]", UpdatedTS: now, IsFiltered: 1,
	}}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}

	if got, err := defaultStore.listPortalIDsWithNewestTimestamp(ctx, 1<<31-1); err != nil {
		t.Fatalf("default candidates: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("default filtered metadata candidates = %#v, want none", got)
	}

	optInStore := newCloudBackfillStore(db, testSQLLoginID, true)
	got, err := optInStore.listPortalIDsWithNewestTimestamp(ctx, 1<<31-1)
	if err != nil {
		t.Fatalf("opted-in candidates: %v", err)
	}
	if len(got) != 1 || got[0].PortalID != portalID || got[0].MetadataTS != now || got[0].MessageCount != 0 || got[0].ContentfulCount != 0 {
		t.Fatalf("opted-in filtered metadata candidates = %#v, want one metadata-only portal", got)
	}
}

func TestOptedInFilteredMessageReadersExcludeDeletedChatSources(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID, true)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	const portalID = "tel:+15550000097"
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{{
		CloudChatID: "deleted-filtered-chat", PortalID: portalID, Service: "iMessage",
		ParticipantsJSON: "[]", UpdatedTS: 1000, IsFiltered: 1,
	}}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE cloud_chat SET deleted=TRUE
		WHERE login_id=$1 AND cloud_chat_id='deleted-filtered-chat'
	`, testSQLLoginID); err != nil {
		t.Fatalf("mark chat deleted: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: "message-from-deleted-chat", RecordName: "record-from-deleted-chat",
		CloudChatID: "deleted-filtered-chat", PortalID: portalID, TimestampMS: 2000,
		Sender: "tel:+15551111111", Text: "must stay deleted", HasBody: true,
	}}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}

	if got, err := store.listPortalIDsWithNewestTimestamp(ctx, 1<<31-1); err != nil {
		t.Fatalf("listPortalIDsWithNewestTimestamp: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("deleted chat source produced opted-in candidates: %#v", got)
	}
	if got, err := store.listLatestMessages(ctx, portalID, 10); err != nil {
		t.Fatalf("listLatestMessages: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("deleted chat source produced opted-in backfill rows: %#v", got)
	}
	if count, err := store.countBackfillableMessages(ctx, portalID, true); err != nil {
		t.Fatalf("countBackfillableMessages: %v", err)
	} else if count != 0 {
		t.Fatalf("countBackfillableMessages = %d, want 0 for deleted chat source", count)
	}
}

func TestMixedFilteredSiblingMessageReadersExcludeFilteredRows(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	const portalID = "tel:+15550000098"
	now := int64(1000)
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{
		{CloudChatID: "chat-unfiltered", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: now, IsFiltered: 0},
		{CloudChatID: "chat-filtered", PortalID: portalID, Service: "SMS", ParticipantsJSON: "[]", UpdatedTS: now, IsFiltered: 1},
	}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}
	attachments := cloudAttachmentGUIDPlaceholdersJSON([]string{"attachment-visible"})
	filteredAttachments := cloudAttachmentGUIDPlaceholdersJSON([]string{"attachment-filtered"})
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "message-visible", RecordName: "record-visible", CloudChatID: "chat-unfiltered", PortalID: portalID, TimestampMS: 2000, Sender: "tel:+15551111111", Text: "visible", HasBody: true},
		{GUID: "attachment-visible", RecordName: "record-attachment-visible", CloudChatID: "chat-unfiltered", PortalID: portalID, TimestampMS: 3000, Sender: "tel:+15551111111", AttachmentsJSON: attachments, HasBody: true},
		{GUID: "message-filtered", RecordName: "record-filtered", CloudChatID: "chat-filtered", PortalID: portalID, TimestampMS: 4000, Sender: "tel:+15551111111", Text: "filtered", HasBody: true},
		{GUID: "attachment-filtered", RecordName: "record-attachment-filtered", CloudChatID: "chat-filtered", PortalID: portalID, TimestampMS: 5000, Sender: "tel:+15551111111", AttachmentsJSON: filteredAttachments, HasBody: true},
		{GUID: "message-empty-chat", RecordName: "record-empty-chat", CloudChatID: "", PortalID: portalID, TimestampMS: 6000, Sender: "tel:+15551111111", Text: "missing source", HasBody: true},
		{GUID: "message-unknown-chat", RecordName: "record-unknown-chat", CloudChatID: "chat-unknown", PortalID: portalID, TimestampMS: 7000, Sender: "tel:+15551111111", Text: "unknown source", HasBody: true},
	}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}

	candidates, err := store.listPortalIDsWithNewestTimestamp(ctx, 1<<31-1)
	if err != nil {
		t.Fatalf("listPortalIDsWithNewestTimestamp: %v", err)
	}
	if len(candidates) != 1 || candidates[0].PortalID != portalID || candidates[0].MessageCount != 2 || candidates[0].ContentfulCount != 2 {
		t.Fatalf("mixed sibling candidates = %#v, want one portal with two unfiltered rows", candidates)
	}

	readers := map[string]func() ([]cloudMessageRow, error){
		"backward": func() ([]cloudMessageRow, error) {
			return store.listBackwardMessages(ctx, portalID, 0, "", 20)
		},
		"forward": func() ([]cloudMessageRow, error) {
			return store.listForwardMessages(ctx, portalID, 0, "", 20)
		},
		"write_activity": func() ([]cloudMessageRow, error) {
			return store.listForwardMessagesByWriteActivity(ctx, portalID, 0, "", 20)
		},
		"latest": func() ([]cloudMessageRow, error) {
			return store.listLatestMessages(ctx, portalID, 20)
		},
		"oldest": func() ([]cloudMessageRow, error) {
			return store.listOldestMessages(ctx, portalID, 20)
		},
	}
	for name, reader := range readers {
		t.Run(name, func(t *testing.T) {
			rows, err := reader()
			if err != nil {
				t.Fatalf("reader failed: %v", err)
			}
			if len(rows) != 2 {
				t.Fatalf("reader returned %d rows (%#v), want only two unfiltered rows", len(rows), rows)
			}
			for _, row := range rows {
				if row.GUID != "message-visible" && row.GUID != "attachment-visible" {
					t.Fatalf("reader returned filtered sibling row %#v", row)
				}
			}
		})
	}

	attachmentRows, err := store.listAllAttachmentMessages(ctx)
	if err != nil {
		t.Fatalf("listAllAttachmentMessages: %v", err)
	}
	if len(attachmentRows) != 1 || attachmentRows[0].GUID != "attachment-visible" {
		t.Fatalf("attachment pre-upload rows = %#v, want only unfiltered attachment", attachmentRows)
	}
	count, err := store.countBackfillableMessages(ctx, portalID, false)
	if err != nil {
		t.Fatalf("countBackfillableMessages: %v", err)
	}
	if count != 2 {
		t.Fatalf("countBackfillableMessages = %d, want 2 unfiltered rows", count)
	}
}

func TestLegacyUnknownSourcesRemainReadableWithOnlyUnfilteredSiblings(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	const portalID = "tel:+15550000097"
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{{
		CloudChatID: "known-unfiltered", PortalID: portalID, Service: "iMessage",
		ParticipantsJSON: "[]", UpdatedTS: 1000,
	}}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{
		{GUID: "legacy-empty", RecordName: "record-empty", CloudChatID: "", PortalID: portalID, TimestampMS: 1000, Text: "empty source", HasBody: true},
		{GUID: "legacy-unknown", RecordName: "record-unknown", CloudChatID: "unknown-source", PortalID: portalID, TimestampMS: 2000, Text: "unknown source", HasBody: true},
	}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}

	rows, err := store.listLatestMessages(ctx, portalID, 10)
	if err != nil {
		t.Fatalf("listLatestMessages: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("legacy all-unfiltered rows = %#v, want both empty and unknown sources", rows)
	}
}

func TestMessageSourceRemappedToAnotherPortalFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	const oldPortal = "tel:+15550000094"
	const newPortal = "tel:+15550000095"
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{
		{CloudChatID: "remapped-source", PortalID: newPortal, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: 2000},
		{CloudChatID: "old-live-source", PortalID: oldPortal, Service: "SMS", ParticipantsJSON: "[]", UpdatedTS: 1000},
	}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: "stale-old-portal", RecordName: "stale-record", CloudChatID: "remapped-source",
		PortalID: oldPortal, TimestampMS: 1000, Text: "stale duplicate-room history", HasBody: true,
	}}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}

	rows, err := store.listLatestMessages(ctx, oldPortal, 10)
	if err != nil {
		t.Fatalf("listLatestMessages: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("remapped source authorized stale portal rows: %#v", rows)
	}
}

func TestRehydrateChatIdentifierUsesNewestEligibleSibling(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	const portalID = "gid:rehydrate-selector"
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{
		{CloudChatID: "eligible-older", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: 1000},
		{CloudChatID: "filtered-newer", PortalID: portalID, Service: "SMS", ParticipantsJSON: "[]", UpdatedTS: 4000, IsFiltered: 1},
		{CloudChatID: "deleted-newest", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: 5000},
		{CloudChatID: "eligible-newer", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: 3000},
	}); err != nil {
		t.Fatalf("upsertChatBatch: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE cloud_chat SET deleted=TRUE WHERE login_id=$1 AND cloud_chat_id='deleted-newest'`, testSQLLoginID); err != nil {
		t.Fatalf("delete newest sibling: %v", err)
	}
	if got := store.getRehydrateChatIdentifierByPortalID(ctx, portalID); got != "eligible-newer" {
		t.Fatalf("rehydrate chat identifier = %q, want newest live unfiltered sibling", got)
	}
}

func TestSyntheticChatRowsPreserveLegacyMessageFallback(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	const portalID = "tel:+15550000096"
	if err := store.upsertMessageBatch(ctx, []cloudMessageRow{{
		GUID: "legacy-message", RecordName: "legacy-record", CloudChatID: "legacy-chat-without-metadata",
		PortalID: portalID, TimestampMS: 1000, Sender: "tel:+15551111111", Text: "legacy history", HasBody: true,
	}}); err != nil {
		t.Fatalf("upsertMessageBatch: %v", err)
	}
	store.markForwardBackfillDone(ctx, portalID)
	if _, err := db.Exec(ctx, `
		INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, display_name, deleted, created_ts)
		VALUES ($1, 'recycle:' || $2, $2, '', FALSE, $3)
	`, testSQLLoginID, portalID, int64(1)); err != nil {
		t.Fatalf("insert recycle row: %v", err)
	}

	var pseudoRows int
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*) FROM cloud_chat
		WHERE login_id=$1 AND portal_id=$2
		  AND (cloud_chat_id LIKE 'synthetic:%' OR cloud_chat_id LIKE 'recycle:%')
	`, testSQLLoginID, portalID).Scan(&pseudoRows); err != nil {
		t.Fatalf("count pseudo rows: %v", err)
	}
	if pseudoRows != 2 {
		t.Fatalf("pseudo cloud_chat rows = %d, want 2", pseudoRows)
	}

	rows, err := store.listLatestMessages(ctx, portalID, 10)
	if err != nil {
		t.Fatalf("listLatestMessages: %v", err)
	}
	if len(rows) != 1 || rows[0].GUID != "legacy-message" {
		t.Fatalf("legacy rows after synthetic marker = %#v, want message preserved", rows)
	}
	candidates, err := store.listPortalIDsWithNewestTimestamp(ctx, 1<<31-1)
	if err != nil {
		t.Fatalf("listPortalIDsWithNewestTimestamp: %v", err)
	}
	if len(candidates) != 1 || candidates[0].PortalID != portalID || candidates[0].ContentfulCount != 1 {
		t.Fatalf("legacy candidates after synthetic marker = %#v, want one contentful portal", candidates)
	}
}

func TestAttachmentGUIDPlaceholdersCountAsContentfulMessages(t *testing.T) {
	ctx := context.Background()
	rawDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	db, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := newCloudBackfillStore(db, networkid.UserLoginID("login"))
	if err = store.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	attachmentsJSON := cloudAttachmentGUIDPlaceholdersJSON([]string{"att-guid-1"})
	if attachmentsJSON == "" {
		t.Fatal("cloudAttachmentGUIDPlaceholdersJSON returned empty JSON")
	}
	now := int64(1000)
	if _, err = db.Exec(ctx, `
		INSERT INTO cloud_message (
			login_id, guid, portal_id, timestamp_ms, sender, is_from_me, text, subject, record_name,
			tapback_type, tapback_target_guid, attachments_json, has_body, body_scrubbed, created_ts, updated_ts
		)
		VALUES
			($1, 'attachment-only', 'tel:+15550000020', 2000, 'tel:+15551111111', FALSE, '', '', 'record-att', NULL, NULL, $2, TRUE, FALSE, $3, $3)
	`, store.loginID, attachmentsJSON, now); err != nil {
		t.Fatal(err)
	}

	hasMessages, err := store.hasContentfulMessages(ctx, "tel:+15550000020")
	if err != nil {
		t.Fatal(err)
	}
	if !hasMessages {
		t.Fatal("hasContentfulMessages = false, want true for attachment GUID placeholder row")
	}
}

func TestAttachmentPlaceholderNoticeUsesNonCollidingMessageID(t *testing.T) {
	attachmentsJSON := cloudAttachmentGUIDPlaceholdersJSON([]string{"att-guid-1"})
	if attachmentsJSON == "" {
		t.Fatal("cloudAttachmentGUIDPlaceholdersJSON returned empty JSON")
	}
	client := &IMClient{}
	rows := client.cloudRowToBackfillMessages(context.Background(), cloudMessageRow{
		GUID:            "message-guid-1",
		PortalID:        "tel:+15550000020",
		TimestampMS:     2000,
		Sender:          "tel:+15551111111",
		AttachmentsJSON: attachmentsJSON,
		HasBody:         true,
	}, "")
	if len(rows) != 1 {
		t.Fatalf("cloudRowToBackfillMessages returned %d rows, want 1 notice", len(rows))
	}
	if rows[0].ID == makeMessageID("message-guid-1") {
		t.Fatalf("placeholder notice used real message ID %q", rows[0].ID)
	}
	if rows[0].ID != cloudAttachmentNoticeMessageID("message-guid-1") {
		t.Fatalf("placeholder notice ID = %q, want %q", rows[0].ID, cloudAttachmentNoticeMessageID("message-guid-1"))
	}
}

func TestCloudRowToBackfillMessagesSkipsPaddedRenameNotice(t *testing.T) {
	client := &IMClient{}
	rows := client.cloudRowToBackfillMessages(context.Background(), cloudMessageRow{
		GUID:        "message-guid-rename",
		PortalID:    "gid:rename",
		TimestampMS: 2000,
		Sender:      "tel:+15551111111",
		Text:        "Family",
		HasBody:     true,
	}, "Family \uFFFC")
	if len(rows) != 0 {
		t.Fatalf("cloudRowToBackfillMessages returned %d rows for padded rename notice, want 0", len(rows))
	}
}

func TestPortalsFullyBackfilledNoNewContentChecksChatMetadata(t *testing.T) {
	ctx := context.Background()
	rawDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	db, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := newCloudBackfillStore(db, networkid.UserLoginID("login"))
	if err = store.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `
		CREATE TABLE backfill_task (
			user_login_id TEXT NOT NULL,
			portal_id TEXT NOT NULL,
			is_done INTEGER NOT NULL,
			completed_at BIGINT NOT NULL
		)
	`); err != nil {
		t.Fatal(err)
	}

	completedAtMS := int64(1000)
	if _, err = db.Exec(ctx, `
		INSERT INTO backfill_task (user_login_id, portal_id, is_done, completed_at)
		VALUES
			($1, 'tel:+15550000031', 1, $2),
			($1, 'tel:+15550000032', 1, $2)
	`, store.loginID, completedAtMS*1_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `
		INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, display_name, created_ts, updated_ts, deleted, is_filtered)
		VALUES
			($1, 'unchanged-chat', 'tel:+15550000031', 'Old Name', 900, 900, FALSE, 0),
			($1, 'updated-chat', 'tel:+15550000032', 'New Name', 900, 1500, FALSE, 0)
	`, store.loginID); err != nil {
		t.Fatal(err)
	}

	skip, err := store.portalsFullyBackfilledNoNewContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !skip["tel:+15550000031"] {
		t.Fatalf("unchanged portal missing from skip set: %#v", skip)
	}
	if skip["tel:+15550000032"] {
		t.Fatalf("metadata-updated portal included in skip set: %#v", skip)
	}
}

func TestListPortalIDsWithNewestTimestampRespectsInitialBackfillCap(t *testing.T) {
	ctx := context.Background()
	rawDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	db, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := newCloudBackfillStore(db, networkid.UserLoginID("login"))
	if err = store.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	now := int64(1000)
	if _, err = db.Exec(ctx, `
		INSERT INTO cloud_message (
			login_id, guid, portal_id, timestamp_ms, sender, is_from_me, text, record_name,
			tapback_type, tapback_target_guid, attachments_json, has_body, body_scrubbed, created_ts, updated_ts
		)
		VALUES
			($1, 'old-content', 'tel:+15550000010', 1000, 'tel:+15551111111', FALSE, 'old but real', 'record-old', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'new-empty', 'tel:+15550000010', 2000, 'tel:+15551111111', FALSE, '', 'record-empty', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'new-reaction', 'tel:+15550000010', 3000, 'tel:+15551111111', FALSE, '', 'record-reaction', 2000, 'old-content', '', TRUE, FALSE, $2, $2),
			($1, 'window-content', 'tel:+15550000011', 2500, 'tel:+15551111111', FALSE, 'inside window', 'record-window', NULL, NULL, '', TRUE, FALSE, $2, $2),
			($1, 'window-empty', 'tel:+15550000011', 3500, 'tel:+15551111111', FALSE, '', 'record-window-empty', NULL, NULL, '', TRUE, FALSE, $2, $2)
	`, store.loginID, now); err != nil {
		t.Fatal(err)
	}

	got, err := store.listPortalIDsWithNewestTimestamp(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d portals (%#v), want both portals with readable rows in capped window", len(got), got)
	}
	if got[0].PortalID != "tel:+15550000010" || got[0].ActivityTS != 3000 || got[0].NewestTS != 0 || got[0].MessageCount != 2 || got[0].ContentfulCount != 0 {
		t.Fatalf("got portal %#v, want unwindowed readable activity with no capped-window content", got[0])
	}
	if got[0].MessageActivityTS != 3000 {
		t.Fatalf("got portal message activity %#v, want reaction timestamp 3000", got[0])
	}
	hasAnyContent, err := store.hasContentfulMessages(ctx, "tel:+15550000010")
	if err != nil {
		t.Fatal(err)
	}
	if !hasAnyContent {
		t.Fatal("hasContentfulMessages = false, want true for older content outside capped window")
	}
	hasWindowContent, err := store.hasContentfulMessagesInLatestWindow(ctx, "tel:+15550000010", 2)
	if err != nil {
		t.Fatal(err)
	}
	if hasWindowContent {
		t.Fatal("hasContentfulMessagesInLatestWindow = true, want false when only older content is outside capped window")
	}
	if got[1].PortalID != "tel:+15550000011" || got[1].ActivityTS != 2500 || got[1].NewestTS != 2500 || got[1].MessageCount != 1 || got[1].ContentfulCount != 1 {
		t.Fatalf("got portal %#v, want capped-window content portal", got[1])
	}
	hasWindowContent, err = store.hasContentfulMessagesInLatestWindow(ctx, "tel:+15550000011", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWindowContent {
		t.Fatal("hasContentfulMessagesInLatestWindow = false, want true for content inside capped window")
	}

	got, err = store.listPortalIDsWithNewestTimestamp(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d portals (%#v), want both portals once older content is inside capped window", len(got), got)
	}
	if got[0].PortalID != "tel:+15550000010" || got[1].PortalID != "tel:+15550000011" {
		t.Fatalf("got portals %#v, want ordered capped-window portals", got)
	}
	if got[0].ContentfulCount != 1 {
		t.Fatalf("got first portal contentful count %d, want 1 once older content is inside capped window", got[0].ContentfulCount)
	}
	if got[0].ActivityTS != 3000 || got[0].NewestTS != 1000 {
		t.Fatalf("got first portal activity/newest %#v, want reaction activity with contentful message watermark", got[0])
	}
	if got[0].MessageActivityTS != 3000 {
		t.Fatalf("got first portal message activity %#v, want reaction timestamp 3000", got[0])
	}
}

func TestListForwardMessagesByWriteActivityFindsLateArrivalsBeforeAnchor(t *testing.T) {
	ctx := context.Background()
	rawDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	db, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := newCloudBackfillStore(db, networkid.UserLoginID("login"))
	if err = store.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `
		CREATE TABLE backfill_task (
			user_login_id TEXT NOT NULL,
			portal_id TEXT NOT NULL,
			is_done INTEGER NOT NULL,
			completed_at BIGINT NOT NULL
		)
	`); err != nil {
		t.Fatal(err)
	}

	if _, err = db.Exec(ctx, `
		INSERT INTO cloud_message (
			login_id, guid, portal_id, timestamp_ms, sender, is_from_me, text, record_name,
			tapback_type, tapback_target_guid, attachments_json, has_body, body_scrubbed, created_ts, updated_ts
		)
		VALUES
			($1, 'already-seen', 'tel:+15550000012', 9000, 'tel:+15551111111', FALSE, 'old', 'record-old', NULL, NULL, '', TRUE, FALSE, 1000, 1000),
			($1, 'late-reaction', 'tel:+15550000012', 2000, 'tel:+15551111111', FALSE, '', 'record-late', 2000, 'already-seen', '', TRUE, FALSE, 9000, 9000)
	`, store.loginID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `
		WITH RECURSIVE seq(n) AS (
			SELECT 0
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < 599
		)
		INSERT INTO cloud_message (
			login_id, guid, portal_id, timestamp_ms, sender, is_from_me, text, record_name,
			tapback_type, tapback_target_guid, attachments_json, has_body, body_scrubbed, created_ts, updated_ts
		)
		SELECT
			$1,
			printf('cached-%03d', n),
			'tel:+15550000012',
			100 + n,
			'tel:+15551111111',
			FALSE,
			'cached',
			printf('record-cached-%03d', n),
			NULL,
			NULL,
			'',
			TRUE,
			FALSE,
			1000,
			1000
		FROM seq
	`, store.loginID); err != nil {
		t.Fatal(err)
	}
	completedAtMS := int64(5000)
	if _, err = db.Exec(ctx, `
		INSERT INTO backfill_task (user_login_id, portal_id, is_done, completed_at)
		VALUES ($1, 'tel:+15550000012', 1, $2)
	`, store.loginID, completedAtMS*1_000_000); err != nil {
		t.Fatal(err)
	}

	rows, err := store.listForwardMessages(ctx, "tel:+15550000012", 9000, "already-seen", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("timestamp forward query returned %#v, want no rows before anchor", rows)
	}
	rows, err = store.listForwardMessagesByWriteActivity(ctx, "tel:+15550000012", 1000, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].GUID != "late-reaction" || rows[0].WriteActivityTS != 9000 {
		t.Fatalf("write-activity forward query returned %#v, want late reaction", rows)
	}
	rows, err = store.listForwardMessagesByWriteActivity(ctx, "tel:+15550000012", 0, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 500 || rows[len(rows)-1].GUID == "late-reaction" {
		t.Fatalf("unseeded capped write-activity query returned %d rows ending with %q, want cap spent before late reaction", len(rows), rows[len(rows)-1].GUID)
	}
	watermark, err := store.completedBackfillWriteWatermark(ctx, "tel:+15550000012")
	if err != nil {
		t.Fatal(err)
	}
	if watermark != completedAtMS {
		t.Fatalf("completedBackfillWriteWatermark = %d, want %d", watermark, completedAtMS)
	}
	rows, err = store.listForwardMessagesByWriteActivity(ctx, "tel:+15550000012", watermark, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].GUID != "late-reaction" || rows[0].WriteActivityTS != 9000 {
		t.Fatalf("seeded capped write-activity query returned %#v, want late reaction", rows)
	}
}

func TestLiveMessageHasTextMatchesConversionInputs(t *testing.T) {
	text := func(s string) *string { return &s }
	tests := []struct {
		name string
		msg  rustpushgo.WrappedMessage
		want bool
	}{
		{
			name: "plain text",
			msg:  rustpushgo.WrappedMessage{Text: text("hello")},
			want: true,
		},
		{
			name: "object placeholder only",
			msg:  rustpushgo.WrappedMessage{Text: text("\ufffc\n ")},
			want: false,
		},
		{
			name: "tab only",
			msg:  rustpushgo.WrappedMessage{Text: text("\t")},
			want: false,
		},
		{
			name: "subject only",
			msg:  rustpushgo.WrappedMessage{Subject: text("subject")},
			want: true,
		},
		{
			name: "whitespace subject",
			msg:  rustpushgo.WrappedMessage{Subject: text(" \n\t")},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := liveMessageHasText(tt.msg); got != tt.want {
				t.Fatalf("liveMessageHasText() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizedBackfillText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "plain", text: "hello", want: "hello"},
		{name: "object placeholder only", text: "\uFFFC \n\t", want: ""},
		{name: "tabs trimmed", text: "\tmessage\t", want: "message"},
		{name: "placeholder inside", text: "a\uFFFCb", want: "ab"},
		{name: "non ascii whitespace trimmed", text: "\u00A0", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedBackfillText(tt.text); got != tt.want {
				t.Fatalf("normalizedBackfillText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizedBackfillSubject(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		want    string
	}{
		{name: "plain", subject: "subject", want: "subject"},
		{name: "ascii whitespace trimmed", subject: " \t\nsubject\r", want: "subject"},
		{name: "ascii whitespace only", subject: " \t\n\r", want: ""},
		{name: "non ascii whitespace trimmed", subject: "\u00A0", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedBackfillSubject(tt.subject); got != tt.want {
				t.Fatalf("normalizedBackfillSubject() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParticipantSetsMatch(t *testing.T) {
	self := "tel:+15551234567"
	selfEmail := "mailto:user@example.com"
	// isSelf checks against all known handles (phone + email).
	isSelf := func(h string) bool {
		n := normalizeIdentifierForPortalID(h)
		return n == normalizeIdentifierForPortalID(self) || n == normalizeIdentifierForPortalID(selfEmail)
	}

	tests := []struct {
		name   string
		a, b   []string
		isSelf func(string) bool
		want   bool
	}{
		{
			name:   "identical sets",
			a:      []string{"tel:+15551111111", "tel:+15552222222", self},
			b:      []string{"tel:+15552222222", "tel:+15551111111", self},
			isSelf: isSelf,
			want:   true,
		},
		{
			name:   "self in a but not b",
			a:      []string{"tel:+15551111111", self},
			b:      []string{"tel:+15551111111"},
			isSelf: isSelf,
			want:   true,
		},
		{
			name:   "self in b but not a",
			a:      []string{"tel:+15551111111"},
			b:      []string{"tel:+15551111111", self},
			isSelf: isSelf,
			want:   true,
		},
		{
			name:   "self email handle in a, phone handle absent",
			a:      []string{"tel:+15551111111", selfEmail},
			b:      []string{"tel:+15551111111"},
			isSelf: isSelf,
			want:   true,
		},
		{
			name:   "non-self member differs",
			a:      []string{"tel:+15551111111", "tel:+15552222222"},
			b:      []string{"tel:+15551111111", "tel:+15553333333"},
			isSelf: isSelf,
			want:   false,
		},
		{
			name:   "diff is 1 but differing member is not self",
			a:      []string{"tel:+15551111111", "tel:+15552222222", "tel:+15554444444"},
			b:      []string{"tel:+15551111111", "tel:+15552222222"},
			isSelf: isSelf,
			want:   false,
		},
		{
			name:   "both empty",
			a:      []string{},
			b:      []string{},
			isSelf: isSelf,
			want:   false,
		},
		{
			name:   "empty set a",
			a:      []string{},
			b:      []string{"tel:+15551111111"},
			isSelf: isSelf,
			want:   false,
		},
		{
			name:   "empty set b",
			a:      []string{"tel:+15551111111"},
			b:      []string{},
			isSelf: isSelf,
			want:   false,
		},
		{
			name:   "nil isSelf disallows any difference",
			a:      []string{"tel:+15551111111", self},
			b:      []string{"tel:+15551111111"},
			isSelf: nil,
			want:   false,
		},
		{
			name:   "both diffs are self handles (phone vs email)",
			a:      []string{"tel:+15551111111", self},
			b:      []string{"tel:+15551111111", selfEmail},
			isSelf: isSelf,
			want:   true,
		},
		{
			name:   "duplicates in input",
			a:      []string{"tel:+15551111111", "tel:+15551111111"},
			b:      []string{"tel:+15551111111"},
			isSelf: isSelf,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := participantSetsMatch(tt.a, tt.b, tt.isSelf)
			if got != tt.want {
				t.Errorf("participantSetsMatch(%v, %v) = %v, want %v",
					tt.a, tt.b, got, tt.want)
			}
		})
	}
}
