package connector

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeGroupMessagePortalIDs(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("reject_last_move=%t", fail), func(t *testing.T) {
			ctx := context.Background()
			db := newTestSQLiteDB(t)
			store := newCloudBackfillStore(db, testSQLLoginID)
			if err := store.ensureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			// A cycle, a feeder into that cycle, a chain, and a no-op. Every
			// row must move once from its original portal, never transitively.
			moves := map[string]string{
				"a": "b", "b": "a", "c": "b",
				"d": "e", "e": "f", "self": "self", "z": "reject",
			}
			// More than SQLite's traditional 999 bind limit: the update must
			// not need separate parameters or statements for every mapping.
			for i := 0; i < 600; i++ {
				moves[fmt.Sprintf("source-%03d", i)] = fmt.Sprintf("target-%03d", i)
			}
			var chats []cloudChatUpsertRow
			var messages []cloudMessageRow
			for from, to := range moves {
				chats = append(chats, cloudChatUpsertRow{CloudChatID: from, PortalID: "gid:" + to})
				messages = append(messages, cloudMessageRow{
					GUID: from, PortalID: "gid:" + from, TimestampMS: 1,
				})
			}
			if err := store.upsertChatBatch(ctx, chats); err != nil {
				t.Fatal(err)
			}
			if err := store.upsertMessageBatch(ctx, messages); err != nil {
				t.Fatal(err)
			}
			other := newCloudBackfillStore(db, "other-login")
			if err := other.upsertMessageBatch(ctx, []cloudMessageRow{{GUID: "other", PortalID: "gid:a", TimestampMS: 1}}); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err := db.Exec(ctx, `CREATE TRIGGER reject_portal_move
					BEFORE UPDATE OF portal_id ON cloud_message
					WHEN NEW.portal_id='gid:reject'
					BEGIN SELECT RAISE(ABORT, 'synthetic mapping rejection'); END`); err != nil {
					t.Fatal(err)
				}
			}
			count, err := store.normalizeGroupMessagePortalIDs(ctx)
			if fail {
				if err == nil || count != 0 {
					t.Errorf("failed normalization = %d, %v; want zero committed moves and an error", count, err)
				}
			} else if err != nil || count != int64(len(moves)-1) {
				t.Fatalf("normalization = %d, %v; want %d", count, err, len(moves)-1)
			}
			for from, to := range moves {
				want := "gid:" + to
				if fail {
					want = "gid:" + from
				}
				var got string
				if err := db.QueryRow(ctx, `SELECT portal_id FROM cloud_message WHERE login_id=$1 AND guid=$2`, testSQLLoginID, from).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("message %s portal = %s, want %s", from, got, want)
				}
			}
			var otherPortal string
			if err := db.QueryRow(ctx, `SELECT portal_id FROM cloud_message WHERE login_id=$1 AND guid='other'`, "other-login").Scan(&otherPortal); err != nil || otherPortal != "gid:a" {
				t.Fatalf("other login portal = %s, %v; want gid:a", otherPortal, err)
			}
		})
	}
}

func TestNormalizeGroupMessagePortalIDsUsesPortalIndex(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ctx, `EXPLAIN QUERY PLAN
		WITH moves AS MATERIALIZED (
			SELECT original.guid, mapping.value AS portal_id
			FROM json_each($2) AS mapping
			CROSS JOIN cloud_message AS original
			WHERE original.login_id=$1 AND original.portal_id=mapping.key
		)
		UPDATE cloud_message AS cm SET portal_id=moves.portal_id
		FROM moves
		WHERE cm.login_id=$1 AND cm.guid=moves.guid`,
		testSQLLoginID, `{"gid:old":"gid:new"}`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "login_id=? AND portal_id=?") {
		t.Fatalf("normalization lost its keyed portal lookup:\n%s", plan)
	}
}
