package connector

import (
	"context"
	"reflect"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

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
		haveExact      bool
		want           rustpushgo.WrappedConversation
		wantSend       bool
	}{
		{"iMessage notifies contacts", dm, true, true, dm, true},
		{"iMessage without exact message still notifies contacts", dm, true, false, dm, true},
		{"iMessage with read_receipts off syncs own devices", dm, false, true, selfOnly(dm), true},
		{"iMessage group with read_receipts off keeps group identity", group, false, true, selfOnly(group), true},
		{"carrier DM syncs own devices", carrier(dm), true, true, selfOnly(carrier(dm)), true},
		{"carrier DM with read_receipts off syncs own devices", carrier(dm), false, true, selfOnly(carrier(dm)), true},
		{"carrier group keeps group identity", carrier(group), true, true, selfOnly(carrier(group)), true},
		{"carrier without exact message sends nothing", carrier(dm), true, false, carrier(dm), false},
		{"own-devices receipt without exact message sends nothing", dm, false, false, dm, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, send := receiptConversation(tt.conv, self, tt.notifyContacts, tt.haveExact)
			if send != tt.wantSend {
				t.Fatalf("send = %v, want %v", send, tt.wantSend)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("conversation = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// An RCS chat is a carrier portal, so its receipts take the own-devices path.
func TestReceiptConversation_RCSPortalIsCarrier(t *testing.T) {
	if !isCarrierService("RCS") {
		t.Fatal("RCS should be a carrier service")
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
