package connector

import (
	"reflect"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
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
