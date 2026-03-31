package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestShouldCacheSenderGUIDForPortal(t *testing.T) {
	if !shouldCacheSenderGUIDForPortal("tel:+15551234567,mailto:user@example.com", "ABC") {
		t.Fatal("comma-based group portal should cache sender guid")
	}
	if !shouldCacheSenderGUIDForPortal("gid:abcdef", "ABCDEF") {
		t.Fatal("canonical gid portal should cache its own sender guid")
	}
	if shouldCacheSenderGUIDForPortal("gid:canonical-guid", "alias-guid") {
		t.Fatal("aliased gid portal should not overwrite the canonical sender guid")
	}
	if shouldCacheSenderGUIDForPortal("tel:+15551234567", "ABC") {
		t.Fatal("dm portal should not cache sender guid")
	}
}

func TestResolveInBatchTapbackTargetPrefersAttachmentMessage(t *testing.T) {
	textMsg := &bridgev2.BackfillMessage{ID: makeMessageID("guid")}
	attMsg := &bridgev2.BackfillMessage{ID: makeMessageID("guid_att0")}
	messageByGUID := map[string]*bridgev2.BackfillMessage{
		"guid": textMsg,
	}
	messageByID := map[networkid.MessageID]*bridgev2.BackfillMessage{
		textMsg.ID: textMsg,
		attMsg.ID:  attMsg,
	}

	target, targetPart, ok := resolveInBatchTapbackTarget(messageByGUID, messageByID, "guid", 1)
	if !ok {
		t.Fatal("expected in-batch attachment target to resolve")
	}
	if target != attMsg {
		t.Fatalf("expected attachment message target, got %#v", target)
	}
	if targetPart != nil {
		t.Fatalf("expected nil target part for attachment message target, got %#v", targetPart)
	}
}

func TestResolveInBatchTapbackTargetFallsBackToBareGUID(t *testing.T) {
	msg := &bridgev2.BackfillMessage{ID: makeMessageID("guid")}
	messageByGUID := map[string]*bridgev2.BackfillMessage{"guid": msg}
	messageByID := map[networkid.MessageID]*bridgev2.BackfillMessage{msg.ID: msg}

	target, targetPart, ok := resolveInBatchTapbackTarget(messageByGUID, messageByID, "guid", 0)
	if !ok || target != msg || targetPart != nil {
		t.Fatalf("expected bare guid target, got target=%#v part=%#v ok=%v", target, targetPart, ok)
	}
}
