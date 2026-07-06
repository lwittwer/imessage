//go:build darwin

package mac

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	maulogger "maunium.net/go/maulogger/v2"

	"github.com/lrhodin/corten-matrix/imessage"
)

func newTestMacMessagesDB(t *testing.T) *macOSDatabase {
	t.Helper()
	rawDB, err := sql.Open("sqlite3", "file:mac_messages_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	for _, stmt := range []string{
		`CREATE TABLE chat (ROWID INTEGER PRIMARY KEY, guid TEXT, group_id TEXT DEFAULT '')`,
		`CREATE TABLE handle (ROWID INTEGER PRIMARY KEY, id TEXT, service TEXT)`,
		`CREATE TABLE message (
			ROWID INTEGER PRIMARY KEY,
			guid TEXT,
			date INTEGER,
			subject TEXT,
			text TEXT,
			attributedBody BLOB,
			handle_id INTEGER,
			other_handle INTEGER DEFAULT 0,
			is_from_me INTEGER,
			date_read INTEGER DEFAULT 0,
			is_delivered INTEGER DEFAULT 0,
			is_sent INTEGER DEFAULT 0,
			is_emote INTEGER DEFAULT 0,
			is_audio_message INTEGER DEFAULT 0,
			thread_originator_guid TEXT DEFAULT '',
			thread_originator_part TEXT DEFAULT '',
			item_type INTEGER,
			associated_message_guid TEXT,
			associated_message_type INTEGER DEFAULT 0,
			group_title TEXT,
			group_action_type INTEGER DEFAULT 0
		)`,
		`CREATE TABLE chat_message_join (chat_id INTEGER, message_id INTEGER)`,
		`CREATE TABLE attachment (
			ROWID INTEGER PRIMARY KEY,
			guid TEXT,
			filename TEXT,
			mime_type TEXT,
			transfer_name TEXT,
			hide_attachment INTEGER DEFAULT 0,
			created_date INTEGER DEFAULT 0
		)`,
		`CREATE TABLE message_attachment_join (message_id INTEGER, attachment_id INTEGER)`,
	} {
		if _, err = rawDB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	mac := &macOSDatabase{chatDB: rawDB, log: maulogger.Sub("test")}
	mac.messagesBeforeWithLimitQuery, err = rawDB.Prepare(messagesBeforeWithLimitQuery)
	if err != nil {
		t.Fatal(err)
	}
	mac.messagesBeforeCursorQuery, err = rawDB.Prepare(messagesBeforeCursorQuery)
	if err != nil {
		t.Fatal(err)
	}
	mac.attachmentsQuery, err = rawDB.Prepare(attachmentsQuery)
	if err != nil {
		t.Fatal(err)
	}
	mac.hasAnyBackfillableQuery, err = rawDB.Prepare(hasAnyBackfillableQuery)
	if err != nil {
		t.Fatal(err)
	}
	mac.hasInitialBackfillableQuery, err = rawDB.Prepare(hasInitialBackfillableQuery)
	if err != nil {
		t.Fatal(err)
	}
	return mac
}

func TestHasBackfillableMessagesBeforeRespectsInitialWindow(t *testing.T) {
	mac := newTestMacMessagesDB(t)
	if _, err := mac.chatDB.Exec(`
		INSERT INTO chat (ROWID, guid) VALUES
			(1, 'iMessage;-;+15550000001'),
			(2, 'iMessage;-;+15550000002'),
			(3, 'iMessage;-;+15550000003');
		INSERT INTO handle (ROWID, id, service) VALUES (1, '+15551111111', 'iMessage');
		INSERT INTO message (ROWID, guid, date, subject, text, attributedBody, handle_id, is_from_me, item_type, associated_message_guid)
		VALUES
			(1, 'old-content', 1000, '', 'old but real', NULL, 1, 0, 0, ''),
			(2, 'new-empty', 2000, '', '', NULL, 1, 0, 0, ''),
			(3, 'new-tapback', 3000, '', 'liked', NULL, 1, 0, 0, 'old-content'),
			(4, 'attachment-only', 1000, '', '', NULL, 1, 0, 0, '');
		INSERT INTO chat_message_join (chat_id, message_id) VALUES (1, 1), (1, 2), (1, 3), (2, 4);
		INSERT INTO attachment (ROWID, guid, filename, mime_type, transfer_name) VALUES (1, 'att-1', '/tmp/photo.jpg', 'image/jpeg', 'photo.jpg');
		INSERT INTO message_attachment_join (message_id, attachment_id) VALUES (4, 1);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := mac.chatDB.Exec(`
		INSERT INTO message (ROWID, guid, date, subject, text, attributedBody, handle_id, is_from_me, item_type, associated_message_guid)
		VALUES (5, 'sql-false-positive', 1000, '', $1, NULL, 1, 0, 0, '');
	`, "\u00A0"); err != nil {
		t.Fatal(err)
	}
	if _, err := mac.chatDB.Exec(`INSERT INTO chat_message_join (chat_id, message_id) VALUES (3, 5)`); err != nil {
		t.Fatal(err)
	}

	before := imessage.AppleEpoch.Add(10 * time.Second)
	hasMessages, err := mac.HasBackfillableMessagesBefore("iMessage;-;+15550000001", before, 2)
	if err != nil {
		t.Fatal(err)
	}
	if hasMessages {
		t.Fatal("HasBackfillableMessagesBefore(limit=2) = true, want false when latest raw rows convert to nothing")
	}

	hasMessages, err = mac.HasBackfillableMessagesBefore("iMessage;-;+15550000001", before, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMessages {
		t.Fatal("HasBackfillableMessagesBefore(limit=3) = false, want true when older content enters initial window")
	}

	hasMessages, err = mac.HasBackfillableMessagesBefore("iMessage;-;+15550000002", before, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMessages {
		t.Fatal("HasBackfillableMessagesBefore() = false, want true for attachment-only message")
	}

	hasMessages, err = mac.HasBackfillableMessagesBefore("iMessage;-;+15550000003", before, 1)
	if err != nil {
		t.Fatal(err)
	}
	if hasMessages {
		t.Fatal("HasBackfillableMessagesBefore() = true, want false when SQL-only text trims to empty")
	}
}

func TestHasBackfillableMessagesBeforePagesThroughSQLFalsePositives(t *testing.T) {
	mac := newTestMacMessagesDB(t)
	if _, err := mac.chatDB.Exec(`
		INSERT INTO chat (ROWID, guid) VALUES (1, 'iMessage;-;+15550000004');
		INSERT INTO handle (ROWID, id, service) VALUES (1, '+15551111111', 'iMessage');
	`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		rowID := i + 1
		if _, err := mac.chatDB.Exec(`
			INSERT INTO message (ROWID, guid, date, subject, text, attributedBody, handle_id, is_from_me, item_type, associated_message_guid)
			VALUES ($1, $2, $3, '', $4, NULL, 1, 0, 0, '');
		`, rowID, "sql-false-positive", 2000+i, "\u00A0"); err != nil {
			t.Fatal(err)
		}
		if _, err := mac.chatDB.Exec(`INSERT INTO chat_message_join (chat_id, message_id) VALUES (1, $1)`, rowID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mac.chatDB.Exec(`
		INSERT INTO message (ROWID, guid, date, subject, text, attributedBody, handle_id, is_from_me, item_type, associated_message_guid)
		VALUES (1000, 'old-content', 1000, '', 'old but real', NULL, 1, 0, 0, '');
		INSERT INTO chat_message_join (chat_id, message_id) VALUES (1, 1000);
	`); err != nil {
		t.Fatal(err)
	}

	before := imessage.AppleEpoch.Add(10 * time.Second)
	hasMessages, err := mac.HasBackfillableMessagesBefore("iMessage;-;+15550000004", before, 1<<31-1)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMessages {
		t.Fatal("HasBackfillableMessagesBefore() = false, want true after paging past SQL-only content")
	}
}
