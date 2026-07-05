package connector

import (
	"strconv"
	"testing"
	"time"

	"github.com/lrhodin/corten-matrix/imessage"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

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
	if !before.Equal(olderFiltered.Time) {
		t.Fatalf("cursor decoded to %s, want oldest raw message time %s", before, olderFiltered.Time)
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
			{Time: time.Unix(200, 0)},
			{Time: aBoundary},
		},
		chatB: {
			{Time: time.Unix(600, 0)},
			{Time: bBoundary},
		},
	}, 2, false)
	if cursor == "" {
		t.Fatal("encodeChatDBBackfillCursor returned empty cursor for full merged pages")
	}

	times := decodeChatDBBackfillCursor(cursor, []string{chatA, chatB})
	if !times[chatA].Equal(aBoundary) {
		t.Fatalf("cursor for %s = %s, want %s", chatA, times[chatA], aBoundary)
	}
	if !times[chatB].Equal(bBoundary) {
		t.Fatalf("cursor for %s = %s, want %s", chatB, times[chatB], bBoundary)
	}
}

func TestChatDBBackfillCursorAcceptsLegacyGlobalTimestamp(t *testing.T) {
	chatA := "iMessage;-;+15550000001"
	chatB := "iMessage;-;+15550000002"
	before := time.Unix(123, 0)

	times := decodeChatDBBackfillCursor(networkid.PaginationCursor(strconv.FormatInt(before.UnixNano(), 10)), []string{chatA, chatB})
	if !times[chatA].Equal(before) {
		t.Fatalf("legacy cursor for %s = %s, want %s", chatA, times[chatA], before)
	}
	if !times[chatB].Equal(before) {
		t.Fatalf("legacy cursor for %s = %s, want %s", chatB, times[chatB], before)
	}
}
