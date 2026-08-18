package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
)

func TestMatrixReactionToDatabasePreservesMessagePartID(t *testing.T) {
	msg := &bridgev2.MatrixReaction{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.ReactionEventContent]{
			Event:   &event.Event{},
			Content: &event.ReactionEventContent{},
		},
		TargetMessage: &database.Message{
			ID:     "message-guid-1",
			PartID: "att0",
		},
	}

	got := (&IMClient{}).matrixReactionToDatabase(msg)
	if got.MessageID != "message-guid-1" || got.MessagePartID != "att0" {
		t.Fatalf("reaction target = (%q, %q), want (%q, %q)", got.MessageID, got.MessagePartID, "message-guid-1", "att0")
	}
}
