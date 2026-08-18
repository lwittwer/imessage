package connector

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type messageDeleteTestIntent struct {
	bridgev2.MatrixAPI
	errors   map[id.EventID]error
	redacted []id.EventID
}

func (i *messageDeleteTestIntent) GetMXID() id.UserID   { return "@bot:example.org" }
func (i *messageDeleteTestIntent) IsDoublePuppet() bool { return false }
func (i *messageDeleteTestIntent) SendMessage(
	_ context.Context,
	_ id.RoomID,
	_ event.Type,
	content *event.Content,
	_ *bridgev2.MatrixSendExtra,
) (*mautrix.RespSendEvent, error) {
	target := content.Parsed.(*event.RedactionEventContent).Redacts
	i.redacted = append(i.redacted, target)
	if err := i.errors[target]; err != nil {
		return nil, err
	}
	return &mautrix.RespSendEvent{EventID: "$redaction"}, nil
}

func newMessageDeleteTestClient(t *testing.T) (*IMClient, *dbutil.Database) {
	t.Helper()
	ctx := context.Background()
	db := newTestSQLiteDB(t)
	if _, err := db.Exec(ctx, `CREATE TABLE message (
		rowid INTEGER PRIMARY KEY, bridge_id TEXT, id TEXT, mxid TEXT,
		room_id TEXT, room_receiver TEXT)`); err != nil {
		t.Fatal(err)
	}
	bridgeDB := database.New("bridge", database.MetaTypes{}, db)
	return &IMClient{
		Main:      &IMConnector{Bridge: &bridgev2.Bridge{ID: "bridge", DB: bridgeDB}},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "login"}},
	}, db
}

func TestRedactMessagePartsWithBotRetainsRowsOnFailure(t *testing.T) {
	parts := []*database.Message{{MXID: "$one"}, {MXID: "$two"}}
	portal := &bridgev2.Portal{Portal: &database.Portal{MXID: "!room:example.org"}}
	intent := &messageDeleteTestIntent{errors: map[id.EventID]error{"$two": errors.New("forbidden")}}
	deleted := false

	err := redactMessagePartsWithBot(context.Background(), portal, intent, parts, time.Time{}, func(context.Context) error {
		deleted = true
		return nil
	})
	if err == nil {
		t.Fatal("redaction failure returned nil")
	}
	if deleted {
		t.Fatal("bridge rows were deleted after a redaction failure")
	}
	if !slices.Equal(intent.redacted, []id.EventID{"$one", "$two"}) {
		t.Fatalf("redacted events = %v", intent.redacted)
	}
}

func TestAppleDeletedMessagePartsFamilyAndAmbiguity(t *testing.T) {
	ctx := context.Background()
	client, db := newMessageDeleteTestClient(t)
	for i, messageID := range []string{"message", "message_att0", "message_att1_notice", "messageXatt0", "message_other"} {
		if _, err := db.Exec(ctx, `INSERT INTO message VALUES ($1,$2,$3,$4,$5,$6)`,
			i+1, "bridge", messageID, "$event", "portal-a", "login"); err != nil {
			t.Fatal(err)
		}
	}
	portal, parts, err := client.getAppleDeletedMessageParts(ctx, "message")
	if err != nil || portal.ID != "portal-a" || len(parts) != 3 {
		t.Fatalf("family lookup = portal:%v parts:%d err:%v", portal, len(parts), err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO message VALUES (6,'bridge','message_att2','$other','portal-b','login')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = client.getAppleDeletedMessageParts(ctx, "message"); !errors.Is(err, errAppleDeletedMessagePortalAmbiguous) {
		t.Fatalf("cross-portal family error = %v", err)
	}
}

func TestPrepareDeletedMessageRechecksAfterTombstone(t *testing.T) {
	ctx := context.Background()
	client, db := newMessageDeleteTestClient(t)
	client.cloudStore = newCloudBackfillStore(db, "login")
	if err := client.cloudStore.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TRIGGER materialize_message_after_tombstone
		AFTER INSERT ON cloud_message WHEN NEW.deleted=TRUE BEGIN
			INSERT INTO message (bridge_id,id,mxid,room_id,room_receiver)
			VALUES ('bridge',NEW.guid,'$base','portal-a','login');
			INSERT INTO message (bridge_id,id,mxid,room_id,room_receiver)
			VALUES ('bridge',NEW.guid || '_att0','$attachment','portal-a','login');
		END`); err != nil {
		t.Fatal(err)
	}

	portal, parts, err := client.prepareDeletedMessage(ctx, "message")
	if err != nil || portal.ID != "portal-a" || len(parts) != 2 {
		t.Fatalf("refreshed handoff = portal:%v parts:%d err:%v", portal, len(parts), err)
	}
}

func TestAdmitLiveMessageRetriesRoute(t *testing.T) {
	client := &IMClient{}
	t.Run("recovers", func(t *testing.T) {
		attempts := 0
		deleted, retried, persistErr, checkErr := client.admitLiveMessage(context.Background(), func(ctx context.Context) error {
			attempts++
			if attempts == 1 {
				return errors.New("database busy")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("retry context has no deadline")
			}
			return nil
		}, func(context.Context) (bool, error) {
			return false, nil
		})
		if deleted || !retried || persistErr != nil || checkErr != nil || attempts != 2 {
			t.Fatalf("admission = deleted:%v retried:%v persist:%v check:%v attempts:%d", deleted, retried, persistErr, checkErr, attempts)
		}
	})

	t.Run("exhausted", func(t *testing.T) {
		attempts := 0
		_, retried, persistErr, _ := client.admitLiveMessage(context.Background(), func(context.Context) error {
			attempts++
			return errors.New("database unavailable")
		}, func(context.Context) (bool, error) {
			t.Fatal("delete check ran after persistence failure")
			return false, nil
		})
		if !retried || persistErr == nil || attempts != 2 {
			t.Fatalf("retried = %v, persistErr = %v, attempts = %d", retried, persistErr, attempts)
		}
	})
}

func TestAdmitLiveMessageWaitsForDeleteTombstone(t *testing.T) {
	client := &IMClient{}
	client.messageDeleteHandoffMu.Lock() // delete resolved; tombstone not written yet
	tombstoned := false
	attempting := make(chan struct{})
	liveDone := make(chan bool, 1)
	go func() {
		close(attempting)
		deleted, _, _, _ := client.admitLiveMessage(context.Background(),
			func(context.Context) error { return nil },
			func(context.Context) (bool, error) { return tombstoned, nil },
		)
		liveDone <- deleted
	}()
	<-attempting
	select {
	case <-liveDone:
		t.Fatal("live route check ran before tombstone write")
	case <-time.After(20 * time.Millisecond):
	}
	tombstoned = true
	client.messageDeleteHandoffMu.Unlock()
	select {
	case deleted := <-liveDone:
		if !deleted {
			t.Fatal("live route check ran before tombstone write")
		}
	case <-time.After(time.Second):
		t.Fatal("live route check did not complete")
	}
}
