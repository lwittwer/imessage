package connector

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

func TestCarrierRouteUsesMetadataOnlySMSDestination(t *testing.T) {
	const (
		self        = "mailto:self@example.com"
		canonicalID = "mailto:peer@example.com"
		destination = "tel:+15550000002"
	)
	client := &IMClient{
		handle:     self,
		allHandles: []string{self},
	}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: canonicalID},
		Metadata: &PortalMetadata{
			IsSms:          true,
			SMSDestination: destination,
		},
	}}

	conv := client.portalToConversation(portal)
	if !conv.IsSms {
		t.Fatal("metadata-only restored route was not treated as SMS")
	}
	if len(conv.Participants) != 2 || conv.Participants[0] != self || conv.Participants[1] != destination {
		t.Fatalf("participants = %#v, want [%q %q]", conv.Participants, self, destination)
	}
	if got := client.carrierRouteSendTarget(conv); got != destination {
		t.Fatalf("carrier repair target = %q, want exact SMSDestination %q", got, destination)
	}
}

func TestCarrierRouteMemoIncludesConcreteDestination(t *testing.T) {
	client := &IMClient{}
	first := carrierRouteMemoKey{PortalID: "tel:+15550000001", Target: "tel:+15550000002"}
	second := carrierRouteMemoKey{PortalID: first.PortalID, Target: "tel:+15550000003"}

	client.noteVerifiedIMessage(first)
	if !client.recentlyVerifiedIMessage(first) {
		t.Fatal("verified memo was not found for the exact portal and target")
	}
	if client.recentlyVerifiedIMessage(second) {
		t.Fatal("verified memo leaked across an SMSDestination change")
	}

	client.noteCheckedUnreachable(second)
	if !client.recentlyCheckedUnreachable(second) {
		t.Fatal("unreachable memo was not found for the exact portal and target")
	}
	if client.recentlyCheckedUnreachable(first) {
		t.Fatal("unreachable memo leaked to a different destination")
	}

	client.carrierVerifiedAt[first] = time.Now().Add(-carrierCheckTTL)
	if client.recentlyVerifiedIMessage(first) {
		t.Fatal("expired verified memo was reused")
	}
}

func TestCarrierRouteNeverRepairsGroups(t *testing.T) {
	client := &IMClient{allHandles: []string{"mailto:self@example.com"}}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: "tel:+15550000001,tel:+15550000002"},
	}}
	conv := rustpushgo.WrappedConversation{
		Participants: []string{"mailto:self@example.com", "tel:+15550000001", "tel:+15550000002"},
		IsSms:        true,
	}

	if client.checkCarrierRoute(context.Background(), portal, conv) {
		t.Fatal("carrier group was treated as eligible for DM route repair")
	}
}
