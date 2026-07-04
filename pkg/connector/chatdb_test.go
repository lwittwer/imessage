package connector

import (
	"testing"

	"github.com/lrhodin/corten-matrix/imessage"
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
