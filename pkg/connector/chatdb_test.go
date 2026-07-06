package connector

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/lrhodin/corten-matrix/imessage"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

type failingMergedChatDBAPI struct {
	imessage.API
}

func (f failingMergedChatDBAPI) GetMessagesBeforeWithLimit(chatID string, before time.Time, limit int) ([]*imessage.Message, error) {
	if chatID == "iMessage;-;+15550000001" {
		return nil, errors.New("temporary chat.db read failure")
	}
	return []*imessage.Message{{
		GUID:     "message-guid-1",
		ItemType: imessage.ItemTypeMessage,
		Sender:   imessage.Identifier{LocalID: "+15550000001"},
		Text:     "hello",
		Time:     time.Unix(100, 0),
	}}, nil
}

func TestChatDBMessageCanBackfill(t *testing.T) {
	tests := []struct {
		name string
		msg  *imessage.Message
		want bool
	}{
		{
			name: "text",
			msg: &imessage.Message{
				ItemType: imessage.ItemTypeMessage,
				Sender:   imessage.Identifier{LocalID: "+15551234567"},
				Text:     "hello",
			},
			want: true,
		},
		{
			name: "object placeholder only",
			msg: &imessage.Message{
				ItemType: imessage.ItemTypeMessage,
				Sender:   imessage.Identifier{LocalID: "+15551234567"},
				Text:     "\uFFFC \n",
			},
			want: false,
		},
		{
			name: "subject only",
			msg: &imessage.Message{
				ItemType: imessage.ItemTypeMessage,
				Sender:   imessage.Identifier{LocalID: "+15551234567"},
				Subject:  "Topic",
			},
			want: true,
		},
		{
			name: "whitespace subject only",
			msg: &imessage.Message{
				ItemType: imessage.ItemTypeMessage,
				Sender:   imessage.Identifier{LocalID: "+15551234567"},
				Subject:  " \t\n",
			},
			want: false,
		},
		{
			name: "attachment only",
			msg: &imessage.Message{
				ItemType: imessage.ItemTypeMessage,
				Sender:   imessage.Identifier{LocalID: "+15551234567"},
				Attachments: []*imessage.Attachment{{
					FileName: "photo.jpg",
				}},
			},
			want: true,
		},
		{
			name: "from me without sender",
			msg: &imessage.Message{
				ItemType: imessage.ItemTypeMessage,
				IsFromMe: true,
				Text:     "sent",
			},
			want: true,
		},
		{
			name: "inbound without sender",
			msg: &imessage.Message{
				ItemType: imessage.ItemTypeMessage,
				Text:     "senderless",
			},
			want: false,
		},
		{
			name: "tapback",
			msg: &imessage.Message{
				ItemType: imessage.ItemTypeMessage,
				Sender:   imessage.Identifier{LocalID: "+15551234567"},
				Text:     "liked",
				Tapback:  &imessage.Tapback{},
			},
			want: false,
		},
		{
			name: "system item",
			msg: &imessage.Message{
				ItemType: imessage.ItemTypeName,
				Sender:   imessage.Identifier{LocalID: "+15551234567"},
				Text:     "New name",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chatDBMessageCanBackfill(tt.msg); got != tt.want {
				t.Fatalf("chatDBMessageCanBackfill() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChatDBFetchMessagesFailsWholePageOnMergedGUIDError(t *testing.T) {
	portalKey := networkid.PortalKey{
		ID:       networkid.PortalID("tel:+15550000001"),
		Receiver: networkid.UserLoginID("login"),
	}
	client := &IMClient{
		Main: &IMConnector{
			Bridge: &bridgev2.Bridge{
				Config: &bridgeconfig.BridgeConfig{},
			},
		},
	}
	db := &chatDB{api: failingMergedChatDBAPI{}}

	resp, err := db.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{
		Portal: &bridgev2.Portal{
			Portal: &database.Portal{PortalKey: portalKey},
		},
		Count: 2,
	}, client)
	if err == nil {
		t.Fatalf("FetchMessages returned nil error with response %#v, want merged GUID failure", resp)
	}
}

func TestChatDBAttachmentNoticeUsesNonCollidingMessageID(t *testing.T) {
	notice := &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    "Attachment could not be read from chat.db.",
		},
	}}}
	media := &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgImage,
			Body:    "photo.jpg",
		},
	}}}

	if got, want := chatDBAttachmentMessagePartID("message-guid-1", 0, notice), "message-guid-1_att0_notice"; got != want {
		t.Fatalf("notice attachment part ID = %q, want %q", got, want)
	}
	if got, want := chatDBAttachmentMessagePartID("message-guid-1", 0, media), "message-guid-1_att0"; got != want {
		t.Fatalf("media attachment part ID = %q, want %q", got, want)
	}
}

func TestChatDBBackfillCursorAdvancesPastFilteredPage(t *testing.T) {
	newerFiltered := &imessage.Message{Time: time.Unix(100, 0)}
	olderFiltered := &imessage.Message{Time: time.Unix(90, 0)}
	cursor := encodeChatDBBackfillCursor(map[string][]*imessage.Message{
		"iMessage;-;+15551234567": {olderFiltered, newerFiltered},
	}, 2, false)
	if cursor == "" {
		t.Fatal("encodeChatDBBackfillCursor returned empty cursor for full raw page")
	}
	times := decodeChatDBBackfillCursor(cursor, []string{"iMessage;-;+15551234567"})
	before, ok := times["iMessage;-;+15551234567"]
	if !ok {
		t.Fatalf("decodeChatDBBackfillCursor(%q) failed", cursor)
	}
	if before.TimeNS != olderFiltered.Time.UnixNano() {
		t.Fatalf("cursor decoded to %d, want oldest raw message time %d", before.TimeNS, olderFiltered.Time.UnixNano())
	}
	if got := encodeChatDBBackfillCursor(map[string][]*imessage.Message{
		"iMessage;-;+15551234567": {olderFiltered},
	}, 2, false); got != "" {
		t.Fatalf("encodeChatDBBackfillCursor on partial page = %q, want empty", got)
	}
	if got := decodeChatDBBackfillCursor(networkid.PaginationCursor("not-a-timestamp"), []string{"iMessage;-;+15551234567"}); len(got) != 0 {
		t.Fatalf("decodeChatDBBackfillCursor accepted invalid cursor: %#v", got)
	}
}

func TestChatDBBackfillCursorTracksMergedChatGUIDsIndependently(t *testing.T) {
	chatA := "iMessage;-;+15550000001"
	chatB := "iMessage;-;+15550000002"
	aBoundary := time.Unix(100, 0)
	bBoundary := time.Unix(500, 0)

	cursor := encodeChatDBBackfillCursor(map[string][]*imessage.Message{
		chatA: {
			{Time: time.Unix(200, 0), RowID: 201},
			{Time: aBoundary, RowID: 101},
		},
		chatB: {
			{Time: time.Unix(600, 0), RowID: 601},
			{Time: bBoundary, RowID: 501},
		},
	}, 2, false)
	if cursor == "" {
		t.Fatal("encodeChatDBBackfillCursor returned empty cursor for full merged pages")
	}

	times := decodeChatDBBackfillCursor(cursor, []string{chatA, chatB})
	if times[chatA].TimeNS != aBoundary.UnixNano() {
		t.Fatalf("cursor for %s = %d, want %d", chatA, times[chatA].TimeNS, aBoundary.UnixNano())
	}
	if times[chatA].RowID != 101 {
		t.Fatalf("cursor row id for %s = %d, want 101", chatA, times[chatA].RowID)
	}
	if times[chatB].TimeNS != bBoundary.UnixNano() {
		t.Fatalf("cursor for %s = %d, want %d", chatB, times[chatB].TimeNS, bBoundary.UnixNano())
	}
	if times[chatB].RowID != 501 {
		t.Fatalf("cursor row id for %s = %d, want 501", chatB, times[chatB].RowID)
	}
}

func TestChatDBBackfillCursorAcceptsLegacyGlobalTimestamp(t *testing.T) {
	chatA := "iMessage;-;+15550000001"
	chatB := "iMessage;-;+15550000002"
	before := time.Unix(123, 0)

	times := decodeChatDBBackfillCursor(networkid.PaginationCursor(strconv.FormatInt(before.UnixNano(), 10)), []string{chatA, chatB})
	if times[chatA].TimeNS != before.UnixNano() {
		t.Fatalf("legacy cursor for %s = %d, want %d", chatA, times[chatA].TimeNS, before.UnixNano())
	}
	if times[chatB].TimeNS != before.UnixNano() {
		t.Fatalf("legacy cursor for %s = %d, want %d", chatB, times[chatB].TimeNS, before.UnixNano())
	}
}

func TestChatDBBackfillCursorPreservesDuplicateTimestampBoundary(t *testing.T) {
	chatGUID := "iMessage;-;+15550000001"
	sharedTime := time.Unix(100, 0)

	cursor := encodeChatDBBackfillCursor(map[string][]*imessage.Message{
		chatGUID: {
			{Time: sharedTime, RowID: 12},
			{Time: sharedTime, RowID: 11},
		},
	}, 2, false)
	if cursor == "" {
		t.Fatal("encodeChatDBBackfillCursor returned empty cursor for full same-timestamp page")
	}
	times := decodeChatDBBackfillCursor(cursor, []string{chatGUID})
	if times[chatGUID].TimeNS != sharedTime.UnixNano() {
		t.Fatalf("cursor time = %d, want %d", times[chatGUID].TimeNS, sharedTime.UnixNano())
	}
	if times[chatGUID].RowID != 11 {
		t.Fatalf("cursor row id = %d, want 11", times[chatGUID].RowID)
	}
}

func TestChatDBCursorStateTreatsMissingGUIDAsExhausted(t *testing.T) {
	cursorTimes := map[string]chatDBBackfillCursorPosition{
		"iMessage;-;+15550000001": {TimeNS: time.Unix(100, 0).UnixNano(), RowID: 10},
	}

	pos, useCursor, exhausted := chatDBCursorStateForGUID(cursorTimes, "iMessage;-;+15550000001", true)
	if !useCursor || exhausted || pos.RowID != 10 {
		t.Fatalf("cursor state for active GUID = (%#v, %v, %v), want cursor row 10 and not exhausted", pos, useCursor, exhausted)
	}

	_, useCursor, exhausted = chatDBCursorStateForGUID(cursorTimes, "iMessage;-;+15550000002", true)
	if useCursor || !exhausted {
		t.Fatalf("cursor state for missing GUID with active cursor = useCursor %v exhausted %v, want exhausted", useCursor, exhausted)
	}

	_, useCursor, exhausted = chatDBCursorStateForGUID(nil, "iMessage;-;+15550000002", false)
	if useCursor || exhausted {
		t.Fatalf("cursor state without active cursor = useCursor %v exhausted %v, want initial fetch", useCursor, exhausted)
	}
}
