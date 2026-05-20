package connector

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestFetchMessagesRejectsNilPortalBeforeBackendSelection(t *testing.T) {
	resp, err := (&IMClient{}).FetchMessages(context.Background(), bridgev2.FetchMessagesParams{})
	if err != nil {
		t.Fatalf("FetchMessages returned error: %v", err)
	}
	if resp == nil || resp.HasMore || resp.Forward {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestStatusKitInviteTerminalErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "no valid targets", err: errors.New("WrappedError: GenericError: Msg=StatusKit invite: NoValidTargets"), want: true},
		{name: "ids lookup 6001", err: errors.New("WrappedError: GenericError: Msg=StatusKit invite: DoNotRetry(LookupFailed(IDSError(6001)))"), want: true},
		{name: "transient", err: errors.New("temporary network failure"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTerminalStatusKitInviteError(tt.err); got != tt.want {
				t.Fatalf("isTerminalStatusKitInviteError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusKitTerminalFailureFresh(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	if !statusKitTerminalFailureFresh(now.Add(-time.Hour).Format(time.RFC3339)+" NoValidTargets", now) {
		t.Fatal("expected recent terminal failure to be fresh")
	}
	if statusKitTerminalFailureFresh(now.Add(-8*24*time.Hour).Format(time.RFC3339)+" NoValidTargets", now) {
		t.Fatal("expected terminal failure older than TTL to expire")
	}
	if statusKitTerminalFailureFresh("not-a-time NoValidTargets", now) {
		t.Fatal("expected malformed terminal failure timestamp to be ignored")
	}
}
