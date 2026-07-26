package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestStatusKitPortalDedupPanicRollbackKeepsNewerUpdate(t *testing.T) {
	ctx := context.Background()
	portalID := networkid.PortalID("tel:+15550000001")
	persisted := ""
	load := func(context.Context) string { return persisted }
	store := func(_ context.Context, value string) { persisted = value }
	client := &IMClient{}

	installed, firstOwner := client.installStatusKitPortalDedup(
		ctx, portalID, "available", load, store,
	)
	if !installed || firstOwner == 0 || persisted != "available" {
		t.Fatalf("first install = (%v, %d), persisted %q", installed, firstOwner, persisted)
	}

	installed, newerOwner := client.installStatusKitPortalDedup(
		ctx, portalID, "focus", load, store,
	)
	if !installed || newerOwner == 0 || newerOwner == firstOwner || persisted != "focus" {
		t.Fatalf("newer install = (%v, %d), first owner %d, persisted %q", installed, newerOwner, firstOwner, persisted)
	}

	if client.rollbackStatusKitPortalDedup(
		ctx, portalID, "available", firstOwner, load, store,
	) {
		t.Fatal("older panicking handler rolled back a newer canonical update")
	}
	if got, ok := client.statusKitPresenceByPortal.Load(portalID); !ok || got != "focus" {
		t.Fatalf("canonical mode after stale rollback = (%v, %v), want focus", got, ok)
	}
	if persisted != "focus" {
		t.Fatalf("persisted mode after stale rollback = %q, want focus", persisted)
	}

	if !client.rollbackStatusKitPortalDedup(
		ctx, portalID, "focus", newerOwner, load, store,
	) {
		t.Fatal("owning panicking handler did not roll back its canonical update")
	}
	if _, ok := client.statusKitPresenceByPortal.Load(portalID); ok {
		t.Fatal("owning rollback left canonical mode in memory")
	}
	if persisted != "" {
		t.Fatalf("owning rollback left persisted mode %q", persisted)
	}
}

func TestStatusKitPortalDedupRestoredValueHasNoRollbackOwner(t *testing.T) {
	ctx := context.Background()
	portalID := networkid.PortalID("tel:+15550000001")
	persisted := "available"
	load := func(context.Context) string { return persisted }
	store := func(_ context.Context, value string) { persisted = value }
	client := &IMClient{}

	installed, owner := client.installStatusKitPortalDedup(
		ctx, portalID, "available", load, store,
	)
	if installed || owner != 0 {
		t.Fatalf("restored duplicate = (%v, %d), want skipped with no owner", installed, owner)
	}
	if client.rollbackStatusKitPortalDedup(
		ctx, portalID, "available", owner, load, store,
	) {
		t.Fatal("restored duplicate unexpectedly acquired rollback ownership")
	}
	if got, ok := client.statusKitPresenceByPortal.Load(portalID); !ok || got != "available" {
		t.Fatalf("restored canonical mode = (%v, %v), want available", got, ok)
	}
	if persisted != "available" {
		t.Fatalf("restored persisted mode changed to %q", persisted)
	}
}
