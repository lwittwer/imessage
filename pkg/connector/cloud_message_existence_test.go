package connector

import (
	"context"
	"testing"
)

func TestCloudMessageExistence(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	store := newCloudBackfillStore(db, testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	const portalID = "tel:+15550000001"
	check := func(wantReadable, wantContent bool) {
		t.Helper()
		for _, tc := range []struct {
			name  string
			query func(context.Context, string) (bool, error)
			want  bool
		}{
			{"readable", store.hasPortalMessages, wantReadable},
			{"contentful", store.hasContentfulMessages, wantContent},
		} {
			got, err := tc.query(ctx, portalID)
			if err != nil || got != tc.want {
				t.Fatalf("%s = %v, %v; want %v", tc.name, got, err, tc.want)
			}
		}
	}
	check(false, false)
	row := cloudMessageRow{GUID: "message", RecordName: "record", CloudChatID: "source", PortalID: portalID, TimestampMS: 1}
	upsert := func() {
		t.Helper()
		if err := store.upsertMessageBatch(ctx, []cloudMessageRow{row}); err != nil {
			t.Fatal(err)
		}
	}
	upsert()
	check(true, false) // An empty placeholder is readable but has no content.
	row.Text = "synthetic message"
	upsert()
	check(true, true)
	store.loginID = "other-login"
	check(false, false)
	store.loginID = testSQLLoginID
	row.Deleted = true
	upsert()
	check(false, false)
	row.Deleted = false
	row.GUID = "next-message"
	row.RecordName = "next-record"
	upsert()
	chat := cloudChatUpsertRow{CloudChatID: "source", PortalID: portalID, Service: "iMessage", ParticipantsJSON: "[]", UpdatedTS: 2, IsFiltered: 1}
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{chat}); err != nil {
		t.Fatal(err)
	}
	check(false, false)
	store.bridgeFiltered = true
	check(true, true)
	chat.PortalID = "tel:+15550000002"
	if err := store.upsertChatBatch(ctx, []cloudChatUpsertRow{chat}); err != nil {
		t.Fatal(err)
	}
	check(false, false) // Opting in cannot authorize a remapped source.
	if _, err := db.Exec(ctx, `DROP TABLE cloud_message`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []func(context.Context, string) (bool, error){store.hasPortalMessages, store.hasContentfulMessages} {
		if got, err := query(ctx, portalID); err == nil || got {
			t.Fatalf("database failure = %v, %v; want false and an error", got, err)
		}
	}
}
