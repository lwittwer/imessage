package syncstatus

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
)

// The report is shared by the SQLite-backed daemon and external PostgreSQL
// deployments. This regression test keeps the dialect-specific GUID matching
// function in the extracted, CGO-free package instead of accidentally
// reintroducing SQLite's instr() into the PostgreSQL query.
func TestDeliveredGUIDSetUsesDialectSubstringFunction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dialect string
		want    string
		avoid   string
	}{
		{name: "sqlite", dialect: "sqlite3", want: "instr", avoid: "strpos"},
		{name: "postgres", dialect: "postgres", want: "strpos", avoid: "instr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialect, err := dbutil.ParseDialect(tc.dialect)
			if err != nil {
				t.Fatal(err)
			}
			db := &dbutil.Database{Dialect: dialect}
			query := deliveredGUIDSetSQL(db)
			if !strings.Contains(query, tc.want+"(m.id") {
				t.Errorf("deliveredGUIDSetSQL(%s) missing %s(): %s", tc.dialect, tc.want, query)
			}
			if strings.Contains(query, tc.avoid+"(m.id") {
				t.Errorf("deliveredGUIDSetSQL(%s) unexpectedly contains %s(): %s", tc.dialect, tc.avoid, query)
			}
		})
	}
}

func TestFormatRedactsPersistedZoneError(t *testing.T) {
	const (
		secret = "apple-handle:+15551234567"
		url    = "https://icloud.example.invalid/record?authorization=secret"
	)
	r := &SyncStatusReport{
		HasLogin:           true,
		CloudTablesPresent: true,
		BootstrapComplete:  true,
		Zones: []ZoneSyncStatus{{
			Zone:      cloudZoneMessages,
			Present:   true,
			HasError:  true,
			LastError: secret + " " + url,
		}},
	}

	got := r.Format()
	if !strings.Contains(got, safeZoneErrorMessage) {
		t.Fatalf("Format did not expose the static error status:\n%s", got)
	}
	for _, forbidden := range []string{secret, url, "authorization=secret"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("Format leaked persisted CloudKit error text %q:\n%s", forbidden, got)
		}
	}

	// LastError remains source-compatible for callers that construct reports,
	// but its contents must never become terminal or management-room output.
	r.Zones[0].HasError = false
	got = r.Format()
	if strings.Contains(got, secret) || strings.Contains(got, url) {
		t.Fatalf("Format leaked compatibility LastError text:\n%s", got)
	}
}

// The parity fixture below intentionally declares only the columns consumed by
// the shared report. The daemon's schema has more metadata, but these are the
// persisted facts that determine whether a row can still become a Matrix
// event. Keeping all four known non-deliverable shapes in one real SQLite
// query catches accidental drift between the report's buckets.
const syncStatusParitySchema = `
	CREATE TABLE cloud_sync_state (
		login_id TEXT NOT NULL,
		zone TEXT NOT NULL,
		continuation_token TEXT,
		last_success_ts BIGINT,
		last_error TEXT,
		updated_ts BIGINT
	);
	CREATE TABLE cloud_chat (
		login_id TEXT NOT NULL,
		cloud_chat_id TEXT,
		portal_id TEXT NOT NULL,
		is_filtered BOOLEAN,
		deleted BOOLEAN,
		display_name TEXT
	);
	CREATE TABLE cloud_message (
		login_id TEXT NOT NULL,
		guid TEXT NOT NULL,
		chat_id TEXT,
		portal_id TEXT NOT NULL,
		timestamp_ms BIGINT NOT NULL,
		sender TEXT,
		is_from_me BOOLEAN,
		text TEXT,
		subject TEXT,
		deleted BOOLEAN,
		tapback_type INTEGER,
		attachments_json TEXT,
		record_name TEXT,
		body_scrubbed BOOLEAN,
		has_body BOOLEAN
	);
	CREATE TABLE user_login (bridge_id TEXT NOT NULL, id TEXT NOT NULL);
	CREATE TABLE message (
		bridge_id TEXT NOT NULL,
		id TEXT NOT NULL,
		room_receiver TEXT NOT NULL,
		timestamp BIGINT NOT NULL
	);
`

func newSyncStatusParityDB(t *testing.T) *dbutil.Database {
	t.Helper()
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	raw.SetMaxOpenConns(1)
	db, err := dbutil.NewWithDB(raw, "sqlite3")
	if err != nil {
		_ = raw.Close()
		t.Fatalf("wrap SQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := raw.Exec(syncStatusParitySchema); err != nil {
		t.Fatalf("create parity schema: %v", err)
	}
	return db
}

func TestGetSyncStatusPropagatesTableProbeFailures(t *testing.T) {
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	db, err := dbutil.NewWithDB(raw, "sqlite3")
	if err != nil {
		_ = raw.Close()
		t.Fatalf("wrap SQLite: %v", err)
	}
	if err = raw.Close(); err != nil {
		t.Fatalf("close SQLite: %v", err)
	}

	report, err := GetSyncStatus(context.Background(), db, SyncStatusOptions{})
	if err == nil {
		t.Fatalf("GetSyncStatus returned report %#v, want table-probe error", report)
	}
	if !strings.Contains(err.Error(), "failed to inspect the user_login table") {
		t.Fatalf("GetSyncStatus error = %q, want user_login probe classification", err)
	}
}

func TestGetSyncStatusOptInStillExcludesDeletedChatMessages(t *testing.T) {
	const (
		bridgeID = "corten"
		loginID  = "login-deleted-filtered"
	)
	ctx := context.Background()
	db := newSyncStatusParityDB(t)
	if _, err := db.Exec(ctx, `
		INSERT INTO cloud_chat
			(login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'deleted-filtered-chat', 'deleted-filtered-portal', 1, 1, '')
	`, loginID); err != nil {
		t.Fatalf("insert deleted chat: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO cloud_message
			(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
			 text, subject, deleted, tapback_type, attachments_json, record_name,
			 body_scrubbed, has_body)
		VALUES ($1, 'deleted-filtered-guid', 'deleted-filtered-chat',
			'deleted-filtered-portal', 1000, 'tel:+15550001', 0,
			'must stay deleted', '', 0, NULL, '', 'deleted-filtered-record', 0, 1)
	`, loginID); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	report, err := GetSyncStatus(ctx, db, SyncStatusOptions{
		BridgeID:            bridgeID,
		LoginID:             loginID,
		BridgeFilteredChats: true,
	})
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if report.CandidateMessages != 1 || report.FilteredChatMessages != 1 || report.DeliverableMessages != 0 || report.PendingMessages() != 0 {
		t.Fatalf("deleted filtered counts = candidates %d, filtered %d, deliverable %d, pending %d; want 1, 1, 0, 0",
			report.CandidateMessages, report.FilteredChatMessages, report.DeliverableMessages, report.PendingMessages())
	}
}

func TestGetSyncStatusSyntheticRowsPreserveLegacyFallback(t *testing.T) {
	const (
		bridgeID = "corten"
		loginID  = "login-synthetic-legacy"
		portalID = "tel:+15550000096"
	)
	ctx := context.Background()
	db := newSyncStatusParityDB(t)
	if _, err := db.Exec(ctx, `
		INSERT INTO cloud_chat
			(login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'synthetic:' || $2, $2, 0, 0, ''),
		       ($1, 'recycle:' || $2, $2, 0, 0, '')
	`, loginID, portalID); err != nil {
		t.Fatalf("insert pseudo chats: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO cloud_message
			(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
			 text, subject, deleted, tapback_type, attachments_json, record_name,
			 body_scrubbed, has_body)
		VALUES ($1, 'legacy-guid', 'legacy-chat-without-metadata', $2, 1000,
			'tel:+15550001', 0, 'legacy history', '', 0, NULL, '', 'legacy-record', 0, 1)
	`, loginID, portalID); err != nil {
		t.Fatalf("insert legacy message: %v", err)
	}

	report, err := GetSyncStatus(ctx, db, SyncStatusOptions{BridgeID: bridgeID, LoginID: loginID})
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if report.CandidateMessages != 1 || report.FilteredChatMessages != 0 || report.UnavailableMessages != 0 || report.DeliverableMessages != 1 || report.PendingMessages() != 1 {
		t.Fatalf("synthetic legacy counts = candidates %d, filtered %d, unavailable %d, deliverable %d, pending %d; want 1, 0, 0, 1, 1",
			report.CandidateMessages, report.FilteredChatMessages, report.UnavailableMessages, report.DeliverableMessages, report.PendingMessages())
	}
}

func TestGetSyncStatusSQLitePredicateParity(t *testing.T) {
	const (
		bridgeID = "corten"
		loginID  = "login-1"
	)
	ctx := context.Background()
	db := newSyncStatusParityDB(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", strings.TrimSpace(query), err)
		}
	}

	exec(`INSERT INTO user_login (bridge_id, id) VALUES ($1, $2)`, bridgeID, loginID)
	for _, zone := range []string{cloudZoneChats, cloudZoneMessages} {
		exec(`INSERT INTO cloud_sync_state
			(login_id, zone, continuation_token, last_success_ts, last_error, updated_ts)
			VALUES ($1, $2, 'token', 1000, $3, 1000)`, loginID, zone,
			func() any {
				if zone == cloudZoneMessages {
					return "apple-handle:+15551234567 https://icloud.example.invalid/token?secret=1"
				}
				return nil
			}())
	}

	// A deleted filtered sibling must not make this portal look filtered, but its
	// known source chat_id still fails closed for message delivery.
	exec(`INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'chat-deleted-filtered', 'deleted-filtered', 1, 1, '')`, loginID)
	exec(`INSERT INTO cloud_message
		(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
		 text, subject, deleted, tapback_type, attachments_json, record_name,
		 body_scrubbed, has_body)
		VALUES ($1, 'deleted-filtered-guid', 'chat-deleted-filtered', 'deleted-filtered',
		 6000, 'tel:+15550001', 0, 'hello', '', 0, NULL, '', 'record-1', 0, 1)`, loginID)

	// Group rename rows have text but are consumed as portal metadata, not as
	// Matrix message events.
	exec(`INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'chat-group-rename', 'group-rename', 0, 0, 'Renamed' || char(160))`, loginID)
	exec(`INSERT INTO cloud_message
		(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
		 text, subject, deleted, tapback_type, attachments_json, record_name,
		 body_scrubbed, has_body)
		VALUES ($1, 'group-rename-guid', 'chat-group-rename', 'group-rename',
		 5000, 'tel:+15550001', 0, 'Renamed ' || char(65532), '', 0, NULL, '', 'record-2', 0, 1)`, loginID)

	// A has_body=false row with no content is the persisted shape used for
	// participant/system records. Contentful has_body=false rows are covered by
	// TestGetSyncStatusKeepsContentfulHasBodyFalseMessageEligible.
	exec(`INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'chat-no-body', 'no-body', 0, 0, '')`, loginID)
	exec(`INSERT INTO cloud_message
		(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
		 text, subject, deleted, tapback_type, attachments_json, record_name,
		 body_scrubbed, has_body)
		VALUES ($1, 'no-body-guid', 'chat-no-body', 'no-body',
		 4000, 'tel:+15550001', 0, '', '', 0, NULL, '', 'record-3', 0, 0)`, loginID)

	// A legacy row with neither a live chat row nor a CloudKit chat_id still uses
	// the connector's no-metadata fallback and therefore remains pending.
	exec(`INSERT INTO cloud_message
		(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
		 text, subject, deleted, tapback_type, attachments_json, record_name,
		 body_scrubbed, has_body)
		VALUES ($1, 'restore-orphan-guid', '', 'restore-orphan',
		 3000, 'tel:+15550001', 0, 'restored', '', 0, NULL, '', 'record-4', 0, 1)`, loginID)

	// Scrubbing intentionally removes body and sender. Without a matching
	// bridgev2 message row, the report cannot safely call it deliverable.
	exec(`INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'chat-scrubbed-undelivered', 'scrubbed-undelivered', 0, 0, '')`, loginID)
	exec(`INSERT INTO cloud_message
		(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
		 text, subject, deleted, tapback_type, attachments_json, record_name,
		 body_scrubbed, has_body)
		VALUES ($1, 'scrubbed-undelivered-guid', 'chat-scrubbed-undelivered',
		 'scrubbed-undelivered', 2000, '', 0, '', '', 0, NULL, '', 'record-5', 1, 1)`, loginID)

	// A scrubbed row with matching bridgev2 evidence remains a delivered,
	// contentful row; this guards the healthy privacy-scrubber path.
	exec(`INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'chat-scrubbed-delivered', 'scrubbed-delivered', 0, 0, '')`, loginID)
	exec(`INSERT INTO cloud_message
		(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
		 text, subject, deleted, tapback_type, attachments_json, record_name,
		 body_scrubbed, has_body)
		VALUES ($1, 'scrubbed-delivered-guid', 'chat-scrubbed-delivered',
		 'scrubbed-delivered', 1000, '', 0, '', '', 0, NULL, '', 'record-6', 1, 1)`, loginID)
	exec(`INSERT INTO message (bridge_id, id, room_receiver, timestamp)
		VALUES ($1, 'scrubbed-delivered-guid', $2, 1000000000)`, bridgeID, loginID)

	r, err := GetSyncStatus(ctx, db, SyncStatusOptions{BridgeID: bridgeID, LoginID: loginID})
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"candidates", r.CandidateMessages, 6},
		{"filtered", r.FilteredChatMessages, 1},
		{"unavailable", r.UnavailableMessages, 1},
		{"empty/system", r.EmptySystemMessages, 2},
		{"deliverable", r.DeliverableMessages, 2},
		{"delivered", r.DeliveredMessages, 1},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	if r.PendingMessages() != 1 {
		t.Errorf("PendingMessages = %d, want 1 for the legacy no-metadata row", r.PendingMessages())
	}
	if r.ChatsFiltered != 0 {
		t.Errorf("ChatsFiltered = %d, want 0 for a deleted filtered sibling", r.ChatsFiltered)
	}
	if !r.Zones[1].HasError || r.Zones[1].LastError != "" {
		t.Errorf("message zone error state = has=%v raw=%q, want has=true and no raw text", r.Zones[1].HasError, r.Zones[1].LastError)
	}
	output := r.Format()
	for _, forbidden := range []string{"apple-handle:+15551234567", "https://icloud.example.invalid", "secret=1"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("Format leaked a persisted error value %q:\n%s", forbidden, output)
		}
	}
	if !strings.Contains(output, safeZoneErrorMessage) {
		t.Errorf("Format omitted the static redacted zone status:\n%s", output)
	}
}

func TestGetSyncStatusSQLiteMixedFilteredSiblingMessageCounts(t *testing.T) {
	const (
		bridgeID = "corten"
		loginID  = "login-mixed-siblings"
	)
	ctx := context.Background()
	db := newSyncStatusParityDB(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", strings.TrimSpace(query), err)
		}
	}

	exec(`INSERT INTO user_login (bridge_id, id) VALUES ($1, $2)`, bridgeID, loginID)
	exec(`INSERT INTO cloud_chat
		(login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'chat-unfiltered-sibling', 'mixed-sibling-portal', 0, 0, '')`, loginID)
	exec(`INSERT INTO cloud_chat
		(login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'chat-filtered-sibling', 'mixed-sibling-portal', 1, 0, '')`, loginID)

	// Both rows share a portal, but their source chat IDs have different
	// eligibility. A portal-level any-unfiltered predicate would count both as
	// deliverable; the message-level predicate must count only the first.
	for _, row := range []struct {
		guid   string
		chatID string
		text   string
	}{
		{guid: "mixed-unfiltered-guid", chatID: "chat-unfiltered-sibling", text: "visible"},
		{guid: "mixed-filtered-guid", chatID: "chat-filtered-sibling", text: "hidden"},
	} {
		exec(`INSERT INTO cloud_message
			(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
			 text, subject, deleted, tapback_type, attachments_json, record_name,
			 body_scrubbed, has_body)
			VALUES ($1, $2, $3, 'mixed-sibling-portal', $4, 'tel:+15550001', 0,
			 $5, '', 0, NULL, '', $6, 0, 1)`,
			loginID, row.guid, row.chatID, 1000, row.text, "record-"+row.guid)
	}

	r, err := GetSyncStatus(ctx, db, SyncStatusOptions{BridgeID: bridgeID, LoginID: loginID})
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"candidates", r.CandidateMessages, 2},
		{"filtered", r.FilteredChatMessages, 1},
		{"unavailable", r.UnavailableMessages, 0},
		{"empty/system", r.EmptySystemMessages, 0},
		{"deliverable", r.DeliverableMessages, 1},
		{"delivered", r.DeliveredMessages, 0},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	if r.PendingMessages() != 1 {
		t.Errorf("PendingMessages = %d, want 1", r.PendingMessages())
	}
	if r.ChatsIngested != 1 || r.ChatsFiltered != 0 {
		t.Errorf("chat counts = ingested %d filtered %d, want 1 and 0", r.ChatsIngested, r.ChatsFiltered)
	}
}

func TestGetSyncStatusCapIgnoresNewerFilteredSibling(t *testing.T) {
	const (
		bridgeID = "corten"
		loginID  = "login-filtered-cap"
		portalID = "mixed-cap-portal"
	)
	ctx := context.Background()
	db := newSyncStatusParityDB(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", strings.TrimSpace(query), err)
		}
	}

	exec(`INSERT INTO user_login (bridge_id, id) VALUES ($1, $2)`, bridgeID, loginID)
	exec(`INSERT INTO cloud_chat
		(login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'chat-visible', $2, 0, 0, ''),
		       ($1, 'chat-filtered', $2, 1, 0, '')`, loginID, portalID)
	// listLatestMessages applies the source-chat predicate before LIMIT, so the
	// newer filtered sibling must not consume the sole initial-backfill slot.
	exec(`INSERT INTO cloud_message
		(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
		 text, subject, deleted, tapback_type, attachments_json, record_name,
		 body_scrubbed, has_body)
		VALUES ($1, 'newer-filtered', 'chat-filtered', $2, 2000, 'tel:+15550001', 0,
		        'hidden', '', 0, NULL, '', 'record-filtered', 0, 1),
		       ($1, 'older-visible', 'chat-visible', $2, 1000, 'tel:+15550001', 0,
		        'visible', '', 0, NULL, '', 'record-visible', 0, 1)`, loginID, portalID)

	r, err := GetSyncStatus(ctx, db, SyncStatusOptions{
		BridgeID:           bridgeID,
		LoginID:            loginID,
		MaxInitialMessages: 1,
	})
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if r.CandidateMessages != 2 || r.FilteredChatMessages != 1 ||
		r.BeyondCapMessages != 0 || r.DeliverableMessages != 1 || r.PendingMessages() != 1 {
		t.Fatalf("cap counts = candidates %d, filtered %d, beyond %d, deliverable %d, pending %d; want 2, 1, 0, 1, 1",
			r.CandidateMessages, r.FilteredChatMessages, r.BeyondCapMessages,
			r.DeliverableMessages, r.PendingMessages())
	}
}

func TestGetSyncStatusKeepsContentfulHasBodyFalseMessageEligible(t *testing.T) {
	const (
		bridgeID = "corten"
		loginID  = "login-has-body-false"
		portalID = "tel:+15550000097"
	)
	ctx := context.Background()
	db := newSyncStatusParityDB(t)
	if _, err := db.Exec(ctx, `INSERT INTO user_login (bridge_id, id) VALUES ($1, $2)`, bridgeID, loginID); err != nil {
		t.Fatalf("insert login: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO cloud_chat
			(login_id, cloud_chat_id, portal_id, is_filtered, deleted, display_name)
		VALUES ($1, 'chat-restored', $2, 0, 0, '')
	`, loginID, portalID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	// Restored Apple messages can carry real content while has_body remains
	// false. cloudRowToBackfillMessages delivers these; diagnostics must agree.
	if _, err := db.Exec(ctx, `
		INSERT INTO cloud_message
			(login_id, guid, chat_id, portal_id, timestamp_ms, sender, is_from_me,
			 text, subject, deleted, tapback_type, attachments_json, record_name,
			 body_scrubbed, has_body)
		VALUES ($1, 'restored-guid', 'chat-restored', $2, 1000, 'tel:+15550001', 0,
		        'restored text', '', 0, NULL, '', 'restored-record', 0, 0)
	`, loginID, portalID); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	r, err := GetSyncStatus(ctx, db, SyncStatusOptions{BridgeID: bridgeID, LoginID: loginID})
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if r.CandidateMessages != 1 || r.EmptySystemMessages != 0 ||
		r.DeliverableMessages != 1 || r.PendingMessages() != 1 {
		t.Fatalf("has_body=false counts = candidates %d, empty %d, deliverable %d, pending %d; want 1, 0, 1, 1",
			r.CandidateMessages, r.EmptySystemMessages, r.DeliverableMessages, r.PendingMessages())
	}
}
