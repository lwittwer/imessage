package connector

import (
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestConnectorBridgeInfoUsesCanonicalIMessageProtocol(t *testing.T) {
	name := (&IMConnector{}).GetName()
	if name.NetworkID != "imessage" {
		t.Fatalf("NetworkID = %q, want imessage", name.NetworkID)
	}
	if name.BeeperBridgeType != "imessagego" {
		t.Fatalf("BeeperBridgeType = %q, want imessagego", name.BeeperBridgeType)
	}
	if name.NetworkIcon != "mxc://maunium.net/tManJEpANASZvDVzvRvhILdX" {
		t.Fatalf("NetworkIcon = %q, want old iMessage bridge avatar", name.NetworkIcon)
	}

	content := event.BridgeEventContent{Protocol: name.AsBridgeInfoSection()}
	if content.Protocol.ID != "imessagego" {
		t.Fatalf("pre-fill bridge info protocol ID = %q, want imessagego", content.Protocol.ID)
	}
	displayName := content.Protocol.DisplayName
	avatarURL := content.Protocol.AvatarURL
	externalURL := content.Protocol.ExternalURL

	(&IMConnector{}).FillPortalBridgeInfo(nil, &content)
	if content.Protocol.ID != "imessage" {
		t.Fatalf("bridge info protocol ID = %q, want imessage", content.Protocol.ID)
	}
	if content.Protocol.DisplayName != displayName {
		t.Fatalf("bridge info display name = %q, want unchanged %q", content.Protocol.DisplayName, displayName)
	}
	if content.Protocol.AvatarURL != avatarURL {
		t.Fatalf("bridge info avatar URL = %q, want unchanged %q", content.Protocol.AvatarURL, avatarURL)
	}
	if content.Protocol.ExternalURL != externalURL {
		t.Fatalf("bridge info external URL = %q, want unchanged %q", content.Protocol.ExternalURL, externalURL)
	}
}

func TestBridgeInfoVersionBumpedForProtocolIDChange(t *testing.T) {
	info, capabilities := (&IMConnector{}).GetBridgeInfoVersion()
	if info < 2 {
		t.Fatalf("bridge info version = %d, want at least 2", info)
	}
	if capabilities != 1 {
		t.Fatalf("capabilities version = %d, want 1", capabilities)
	}
}
