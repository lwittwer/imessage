package connector

import (
	"context"
	"reflect"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

func TestReceiptConversation(t *testing.T) {
	const self = "mailto:me@example.com"
	groupName := "Weekend"
	senderGuid := "00000000-0000-4000-8000-000000000001"
	dm := rustpushgo.WrappedConversation{
		Participants: []string{"tel:+15550000001", self},
	}
	group := rustpushgo.WrappedConversation{
		Participants: []string{"tel:+15550000001", "tel:+15550000002", self},
		GroupName:    &groupName,
		SenderGuid:   &senderGuid,
	}
	carrier := func(conv rustpushgo.WrappedConversation) rustpushgo.WrappedConversation {
		conv.IsSms = true
		return conv
	}
	selfOnly := func(conv rustpushgo.WrappedConversation) rustpushgo.WrappedConversation {
		conv.Participants = []string{self}
		return conv
	}

	tests := []struct {
		name           string
		conv           rustpushgo.WrappedConversation
		notifyContacts bool
		want           rustpushgo.WrappedConversation
	}{
		{"iMessage notifies contacts", dm, true, dm},
		{"iMessage with read_receipts off syncs own devices", dm, false, selfOnly(dm)},
		{"iMessage group with read_receipts off keeps group identity", group, false, selfOnly(group)},
		{"carrier DM syncs own devices", carrier(dm), true, selfOnly(carrier(dm))},
		{"carrier DM with read_receipts off syncs own devices", carrier(dm), false, selfOnly(carrier(dm))},
		{"carrier group keeps group identity", carrier(group), true, selfOnly(carrier(group))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := receiptConversation(tt.conv, self, tt.notifyContacts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("conversation = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadTargetUUID(t *testing.T) {
	const (
		self  = "mailto:me@example.com"
		alias = "tel:+15559999999"
		peer  = "tel:+15550000001"
		guid  = "00000000-0000-4000-8000-000000000002"
	)
	c := &IMClient{handle: self, allHandles: []string{self, alias}}
	msg := func(id string, sender string) *database.Message {
		return &database.Message{ID: networkid.MessageID(id), SenderID: networkid.UserID(sender)}
	}

	tests := []struct {
		name     string
		msg      *database.Message
		wantUUID string
		wantOK   bool
	}{
		{"incoming message", msg(guid, peer), guid, true},
		{"attachment part strips suffix", msg(guid+"_att1", peer), guid, true},
		{"no message sends nothing", nil, "", false},
		{"own message sends nothing", msg(guid, self), "", false},
		{"own message from another handle sends nothing", msg(guid, alias), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uuid, ok := c.readTargetUUID(tt.msg)
			if uuid != tt.wantUUID || ok != tt.wantOK {
				t.Errorf("readTargetUUID = (%q, %v), want (%q, %v)", uuid, ok, tt.wantUUID, tt.wantOK)
			}
		})
	}
}

func TestGetCapabilities_ReadReceiptsAdvertisedWhenContactReceiptsOff(t *testing.T) {
	c := &IMClient{Main: &IMConnector{Config: IMConfig{ReadReceipts: false, TypingNotifications: true}}}
	for _, roomType := range []database.RoomType{database.RoomTypeDefault, database.RoomTypeDM} {
		portal := &bridgev2.Portal{Portal: &database.Portal{RoomType: roomType}}
		got := c.GetCapabilities(context.Background(), portal)
		if !got.ReadReceipts {
			t.Errorf("room type %q: ReadReceipts should stay advertised", roomType)
		}
		want := caps
		if roomType == database.RoomTypeDM {
			want = capsDM
		}
		if got.ID != want.ID {
			t.Errorf("room type %q: ID = %q, want %q", roomType, got.ID, want.ID)
		}
	}
}
