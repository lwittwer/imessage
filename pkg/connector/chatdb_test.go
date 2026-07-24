package connector

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/lrhodin/corten-matrix/imessage"
	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

type failingMergedChatDBAPI struct {
	imessage.API
}

type recordingExactChatDBAPI struct {
	imessage.API
	calls []string
}

type duplicateMergedChatDBAPI struct {
	imessage.API
}

func (d duplicateMergedChatDBAPI) GetMessagesBeforeWithLimit(chatID string, _ time.Time, _ int) ([]*imessage.Message, error) {
	shared := &imessage.Message{
		GUID:     "shared-message-guid",
		ItemType: imessage.ItemTypeMessage,
		Sender:   imessage.Identifier{LocalID: "+15550000001"},
		Text:     "shared",
		Time:     time.Unix(100, 0),
	}
	unique := &imessage.Message{
		GUID:     "unique-" + chatID,
		ItemType: imessage.ItemTypeMessage,
		Sender:   imessage.Identifier{LocalID: "+15550000001"},
		Text:     "unique",
		Time:     time.Unix(200, 0),
	}
	return []*imessage.Message{shared, unique}, nil
}

func (r *recordingExactChatDBAPI) GetMessagesBeforeWithLimit(chatID string, _ time.Time, _ int) ([]*imessage.Message, error) {
	r.calls = append(r.calls, "backward:"+chatID)
	return []*imessage.Message{{
		GUID:     "message-guid-backward",
		ItemType: imessage.ItemTypeMessage,
		Sender:   imessage.Identifier{LocalID: "+15550000001"},
		Text:     "backward",
		Time:     time.Unix(100, 0),
	}}, nil
}

func (r *recordingExactChatDBAPI) GetMessagesSinceDate(chatID string, _ time.Time, _ string) ([]*imessage.Message, error) {
	r.calls = append(r.calls, "forward:"+chatID)
	return []*imessage.Message{{
		GUID:     "message-guid-forward",
		ItemType: imessage.ItemTypeMessage,
		Sender:   imessage.Identifier{LocalID: "+15550000001"},
		Text:     "forward",
		Time:     time.Unix(200, 0),
	}}, nil
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

func TestChatDBFetchMessagesUsesPersistedExactGUIDForBothDirections(t *testing.T) {
	exactGUID := "SMS;-;+15550000001(smsft)"
	portalKey := networkid.PortalKey{
		ID:       networkid.PortalID("tel:+15550000001"),
		Receiver: networkid.UserLoginID("login"),
	}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: portalKey,
		Metadata:  &PortalMetadata{ChatDBGUIDs: []string{exactGUID}},
	}}
	client := &IMClient{Main: &IMConnector{Bridge: &bridgev2.Bridge{
		Config: &bridgeconfig.BridgeConfig{},
	}}}
	api := &recordingExactChatDBAPI{}
	db := &chatDB{api: api}

	backward, err := db.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{
		Portal: portal,
		Count:  10,
	}, client)
	if err != nil {
		t.Fatalf("backward FetchMessages failed: %v", err)
	}
	if len(backward.Messages) != 1 {
		t.Fatalf("backward FetchMessages returned %d messages, want 1", len(backward.Messages))
	}

	forward, err := db.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{
		Portal:        portal,
		Forward:       true,
		AnchorMessage: &database.Message{Timestamp: time.Unix(150, 0)},
		Count:         10,
		BundledData:   chatDBBackfillGUIDBundle{ChatGUIDs: []string{exactGUID}},
	}, client)
	if err != nil {
		t.Fatalf("forward FetchMessages failed: %v", err)
	}
	if len(forward.Messages) != 1 {
		t.Fatalf("forward FetchMessages returned %d messages, want 1", len(forward.Messages))
	}

	wantCalls := []string{"backward:" + exactGUID, "forward:" + exactGUID}
	if len(api.calls) != len(wantCalls) {
		t.Fatalf("chat.db calls = %#v, want %#v", api.calls, wantCalls)
	}
	for i := range wantCalls {
		if api.calls[i] != wantCalls[i] {
			t.Fatalf("chat.db calls = %#v, want %#v", api.calls, wantCalls)
		}
	}
}

func TestChatDBFetchMessagesDeduplicatesMessagesAcrossExactGUIDs(t *testing.T) {
	exactGUIDs := []string{
		"SMS;-;+15550000001(smsft)",
		"SMS;-;+15550000001(sms)",
	}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{
			ID:       networkid.PortalID("tel:+15550000001"),
			Receiver: networkid.UserLoginID("login"),
		},
		Metadata: &PortalMetadata{ChatDBGUIDs: exactGUIDs},
	}}
	client := &IMClient{Main: &IMConnector{Bridge: &bridgev2.Bridge{
		Config: &bridgeconfig.BridgeConfig{},
	}}}

	resp, err := (&chatDB{api: duplicateMergedChatDBAPI{}}).FetchMessages(
		context.Background(),
		bridgev2.FetchMessagesParams{Portal: portal, Count: 2},
		client,
	)
	if err != nil {
		t.Fatalf("FetchMessages failed: %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("FetchMessages returned %d messages, want shared message once plus two unique messages", len(resp.Messages))
	}
	seen := make(map[networkid.MessageID]bool)
	for _, message := range resp.Messages {
		if seen[message.ID] {
			t.Fatalf("FetchMessages returned duplicate message ID %q", message.ID)
		}
		seen[message.ID] = true
	}
	if resp.Cursor == "" {
		t.Fatal("FetchMessages returned no cursor for full raw pages")
	}
	cursor := decodeChatDBBackfillCursor(resp.Cursor, exactGUIDs)
	for _, exactGUID := range exactGUIDs {
		if _, ok := cursor[exactGUID]; !ok {
			t.Fatalf("cursor %#v does not track exact GUID %q", cursor, exactGUID)
		}
	}
}

func TestChatDBGUIDsForPortalFallsBackForLegacyMetadata(t *testing.T) {
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: networkid.PortalID("tel:+15550000001")},
		Metadata:  &PortalMetadata{},
	}}
	got := (&chatDB{}).chatGUIDsForPortal(portal, &IMClient{}, nil)
	want := portalIDToChatGUIDs("tel:+15550000001")
	if len(got) != len(want) {
		t.Fatalf("legacy GUID fallback = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("legacy GUID fallback = %#v, want %#v", got, want)
		}
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

func TestLiveAttachmentNoticeMarksTransientMetadata(t *testing.T) {
	cm, err := convertAttachment(context.Background(), nil, nil, &attachmentMessage{
		WrappedMessage: &rustpushgo.WrappedMessage{Uuid: "message-guid-1"},
		Attachment: &rustpushgo.WrappedAttachment{
			Filename: "photo.jpg",
			MimeType: "image/jpeg",
			IsInline: true,
		},
		Index: 0,
	}, false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cm == nil || len(cm.Parts) != 1 {
		t.Fatalf("convertAttachment returned %#v, want one notice part", cm)
	}
	meta, ok := cm.Parts[0].DBMetadata.(*MessageMetadata)
	if !ok || !meta.TransientAttachmentNotice {
		t.Fatalf("notice metadata = %#v, want transient attachment notice marker", cm.Parts[0].DBMetadata)
	}
	if cm.Parts[0].Content == nil || cm.Parts[0].Content.MsgType != event.MsgNotice {
		t.Fatalf("notice content = %#v, want m.notice", cm.Parts[0].Content)
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
	chatA := "SMS;-;+15550000001(smsft)"
	chatB := "SMS;-;+15550000001(sms)"
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
