package connector

import (
	"reflect"
	"testing"
	"time"
)

func TestStatusKitModeKey(t *testing.T) {
	focus := "Sleep"
	if got := statusKitModeKey(&focus); got != "Sleep" {
		t.Fatalf("expected focus mode, got %q", got)
	}
	empty := ""
	for _, mode := range []*string{nil, &empty} {
		if got := statusKitModeKey(mode); got != "available" {
			t.Fatalf("expected available, got %q", got)
		}
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
