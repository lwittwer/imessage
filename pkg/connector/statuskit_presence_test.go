package connector

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	_ "github.com/mattn/go-sqlite3"
)

func TestStatusKitModeKey(t *testing.T) {
	focus := "Sleep"
	if got := statusKitModeKey(&focus, false); got != "Sleep" {
		t.Fatalf("expected focus mode, got %q", got)
	}
	if got := statusKitModeKey(&focus, true); got != "available" {
		t.Fatalf("expected available=true to override stale mode, got %q", got)
	}
	empty := ""
	for _, mode := range []*string{nil, &empty} {
		if got := statusKitModeKey(mode, true); got != "available" {
			t.Fatalf("expected available, got %q", got)
		}
	}
	if got := statusKitModeKey(nil, false); got != "unavailable" {
		t.Fatalf("expected unavailable fallback, got %q", got)
	}
}

func TestStatusKitModeSilenced(t *testing.T) {
	tests := map[string]bool{
		"":              false,
		"available":     false,
		"unavailable":   true,
		"Sleep":         true,
		"com.apple.foo": true,
	}
	for mode, want := range tests {
		if got := statusKitModeSilenced(mode); got != want {
			t.Fatalf("statusKitModeSilenced(%q) = %v, want %v", mode, got, want)
		}
	}
}

func TestStatusKitPresenceClockValue(t *testing.T) {
	initial := time.Date(2026, 6, 17, 12, 0, 0, 123000000, time.UTC)
	replay := initial.Add(30 * time.Minute)
	available := initial.Add(2 * time.Hour)

	stored, ok := statusKitPresenceClockValue("available", "Sleep", initial)
	if !ok {
		t.Fatal("available→Sleep transition did not request a clock write")
	}
	if want := "1781697600123"; stored != want {
		t.Fatalf("initial clock value = %q, want %q", stored, want)
	}

	if value, ok := statusKitPresenceClockValue("Sleep", "Sleep", replay); ok {
		stored = value
	}
	if want := "1781697600123"; stored != want {
		t.Fatalf("same-mode replay changed stored clock to %q, want unchanged %q", stored, want)
	}

	if value, ok := statusKitPresenceClockValue("Sleep", "available", available); ok {
		stored = value
	} else {
		t.Fatal("Sleep→available transition did not request a clock write")
	}
	if want := "1781704800123"; stored != want {
		t.Fatalf("available transition clock value = %q, want %q", stored, want)
	}
}

func TestStatusKitRoomNameRepairValueIncludesName(t *testing.T) {
	first := statusKitRoomNameRepairValue("Lauren Thomas")
	second := statusKitRoomNameRepairValue("Lauren T.")
	if first == second {
		t.Fatal("repair marker should change when the bare DM name changes")
	}
	if first != statusKitRoomNameRepairValuePrefix+"Lauren Thomas" {
		t.Fatalf("unexpected repair marker %q", first)
	}
}

func TestNeedsAvailableStatusKitDMRoomNameRepair(t *testing.T) {
	tests := []struct {
		name   string
		portal *bridgev2.Portal
		want   bool
	}{
		{
			name: "nil portal",
		},
		{
			name:   "nil embedded portal",
			portal: &bridgev2.Portal{},
		},
		{
			name: "implicit DM does not need repair",
			portal: &bridgev2.Portal{
				Portal: &database.Portal{
					RoomType: database.RoomTypeDM,
				},
			},
		},
		{
			name: "blanked DM needs repair",
			portal: &bridgev2.Portal{
				Portal: &database.Portal{
					RoomType: database.RoomTypeDM,
					NameSet:  true,
					Name:     "",
				},
			},
			want: true,
		},
		{
			name: "healthy named DM does not need repair",
			portal: &bridgev2.Portal{
				Portal: &database.Portal{
					RoomType: database.RoomTypeDM,
					NameSet:  true,
					Name:     "Lauren Thomas",
				},
			},
		},
		{
			name: "custom DM is owned by the normal title path",
			portal: &bridgev2.Portal{
				Portal: &database.Portal{
					RoomType:     database.RoomTypeDM,
					NameSet:      true,
					NameIsCustom: true,
					Name:         "",
				},
			},
		},
		{
			name: "group does not need DM repair",
			portal: &bridgev2.Portal{
				Portal: &database.Portal{
					RoomType: database.RoomTypeDefault,
					NameSet:  true,
					Name:     "",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsAvailableStatusKitDMRoomNameRepair(tt.portal); got != tt.want {
				t.Fatalf("needsAvailableStatusKitDMRoomNameRepair() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusKitPendingEncodeDecodeAndExpiry(t *testing.T) {
	observedAt := time.Date(2026, 6, 8, 12, 34, 56, 789, time.UTC)
	raw := encodeStatusKitPendingPresence("Work", observedAt)
	pending, ok := decodeStatusKitPendingPresence(raw)
	if !ok {
		t.Fatal("pending state did not decode")
	}
	if pending.Mode != "Work" || !pending.ObservedAt.Equal(observedAt) {
		t.Fatalf("decoded pending mismatch: %#v", pending)
	}
	if statusKitPendingExpired(pending, observedAt.Add(59*time.Minute)) {
		t.Fatal("pending state expired too early")
	}
	if !statusKitPendingExpired(pending, observedAt.Add(time.Hour+time.Nanosecond)) {
		t.Fatal("pending state did not expire after ttl")
	}
	if _, ok := decodeStatusKitPendingPresence(`{"mode":"","observed_at":"2026-06-08T12:34:56Z"}`); ok {
		t.Fatal("malformed pending state decoded successfully")
	}
}

func TestStatusKitAliasVariants(t *testing.T) {
	tests := map[string][]string{
		"+15551234567":     {"+15551234567", "tel:+15551234567"},
		"tel:+15551234567": {"tel:+15551234567", "+15551234567"},
		"user@example.com": {"user@example.com", "mailto:user@example.com"},
		"mailto:u@e.test":  {"mailto:u@e.test", "u@e.test"},
	}
	for input, want := range tests {
		if got := statusKitAliasVariants(input); !reflect.DeepEqual(got, want) {
			t.Fatalf("variants for %q: got %#v, want %#v", input, got, want)
		}
	}
}

func TestStatusKitSelfAliasUsesNormalizedRegisteredHandles(t *testing.T) {
	client := &IMClient{
		allHandles: []string{
			"mailto:LucasWittwer@iCloud.com",
			"+17082801739",
		},
	}

	selfAliases := []string{
		"mailto:lucaswittwer@icloud.com",
		"LucasWittwer@iCloud.com",
		"tel:+17082801739",
		"+17082801739",
	}
	for _, alias := range selfAliases {
		if !client.isStatusKitSelfAlias(alias) {
			t.Fatalf("expected %q to be treated as a StatusKit self alias", alias)
		}
	}

	for _, alias := range []string{"mailto:friend@example.com", "tel:+15551234567"} {
		if client.isStatusKitSelfAlias(alias) {
			t.Fatalf("did not expect %q to be treated as a StatusKit self alias", alias)
		}
	}
}

func TestStatusKitForgetAliasPortalDeletesKVRow(t *testing.T) {
	ctx := context.Background()
	client := makeStatusKitKVTestClient(t)

	client.Main.Bridge.DB.KV.Set(ctx, statusKitAliasPortalKey("mailto:self@example.com"), "tel:+15551234567")
	client.statusKitPortalCache.Store("mailto:self@example.com", networkid.PortalID("tel:+15551234567"))

	client.forgetAliasPortal(ctx, "self@example.com")

	if got := client.Main.Bridge.DB.KV.Get(ctx, statusKitAliasPortalKey("mailto:self@example.com")); got != "" {
		t.Fatalf("expected alias portal KV row to be absent, got value %q", got)
	}
	if rowExists := statusKitKVRowExists(t, client, statusKitAliasPortalKey("mailto:self@example.com")); rowExists {
		t.Fatal("expected forgetAliasPortal to delete the KV row instead of tombstoning it")
	}
	if _, ok := client.statusKitPortalCache.Load("mailto:self@example.com"); ok {
		t.Fatal("expected forgetAliasPortal to remove the in-memory cache entry")
	}
}

func TestStatusKitHydrateDeletesSelfAliasPortalRows(t *testing.T) {
	ctx := context.Background()
	client := makeStatusKitKVTestClient(t)
	client.allHandles = []string{"mailto:self@example.com", "tel:+15550001111"}

	selfAliasKey := statusKitAliasPortalKey("mailto:self@example.com")
	selfValueKey := statusKitAliasPortalKey("mailto:peer@example.com")
	peerKey := statusKitAliasPortalKey("mailto:friend@example.com")
	client.Main.Bridge.DB.KV.Set(ctx, selfAliasKey, "tel:+15551234567")
	client.Main.Bridge.DB.KV.Set(ctx, selfValueKey, "tel:+15550001111")
	client.Main.Bridge.DB.KV.Set(ctx, peerKey, "tel:+15552223333")

	client.hydrateAliasPortalCacheFromKV(ctx, client.UserLogin.Log)

	for _, key := range []database.Key{selfAliasKey, selfValueKey} {
		if rowExists := statusKitKVRowExists(t, client, key); rowExists {
			t.Fatalf("expected hydrateAliasPortalCacheFromKV to delete self mapping row %q", key)
		}
	}
	if got := client.Main.Bridge.DB.KV.Get(ctx, peerKey); got != "tel:+15552223333" {
		t.Fatalf("expected peer mapping to survive hydration, got %q", got)
	}
	if _, ok := client.statusKitPortalCache.Load("mailto:friend@example.com"); !ok {
		t.Fatal("expected peer mapping to be loaded into memory")
	}
}

func makeStatusKitKVTestClient(t *testing.T) *IMClient {
	t.Helper()

	rawDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	rawDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = rawDB.Close()
	})
	if _, err = rawDB.Exec(`CREATE TABLE kv_store (
		bridge_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		PRIMARY KEY (bridge_id, key)
	)`); err != nil {
		t.Fatalf("create kv_store: %v", err)
	}

	db, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		t.Fatalf("wrap sqlite: %v", err)
	}
	bridgeDB := database.New(networkid.BridgeID("test-bridge"), database.MetaTypes{}, db)
	return &IMClient{
		Main: &IMConnector{
			Bridge: &bridgev2.Bridge{
				ID: networkid.BridgeID("test-bridge"),
				DB: bridgeDB,
			},
		},
		UserLogin: &bridgev2.UserLogin{},
	}
}

func statusKitKVRowExists(t *testing.T, client *IMClient, key database.Key) bool {
	t.Helper()

	var exists bool
	err := client.Main.Bridge.DB.QueryRow(
		context.Background(),
		"SELECT EXISTS(SELECT 1 FROM kv_store WHERE bridge_id=$1 AND key=$2)",
		client.Main.Bridge.DB.BridgeID,
		key,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("query kv row %q: %v", key, err)
	}
	return exists
}
