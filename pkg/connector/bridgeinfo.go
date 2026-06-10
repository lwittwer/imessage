package connector

import (
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

var _ bridgev2.PortalBridgeInfoFillingNetwork = (*IMConnector)(nil)

func (c *IMConnector) FillPortalBridgeInfo(_ *bridgev2.Portal, content *event.BridgeEventContent) {
	content.Protocol.ID = c.GetName().NetworkID
}
