package connector

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lrhodin/corten-matrix/imessage"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestMatchChatDBGUIDsToExistingPortalPreservesSuffixVariants(t *testing.T) {
	entries := []chatDBGUIDRefreshEntry{
		{PortalID: "tel:+15550000001", ChatGUID: "SMS;-;+15550000001(smsft)"},
		{PortalID: "tel:+15550000001", ChatGUID: "SMS;-;+15550000001(sms)"},
		{PortalID: "tel:+15550000002", ChatGUID: "iMessage;-;+15550000002"},
	}
	existing := map[string]existingDMPortalCandidate{
		"tel:+15550000001": {ID: "tel:+15550000001", HasMessages: true},
	}
	got := matchChatDBGUIDsToExistingPortals(
		entries,
		contactLookupForTests(),
		nil,
		func(portalID string) existingDMPortalCandidate { return existing[portalID] },
	)
	want := map[string][]string{
		"tel:+15550000001": {
			"SMS;-;+15550000001(sms)",
			"SMS;-;+15550000001(smsft)",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exact GUID assignments = %#v, want %#v", got, want)
	}
}

func TestMatchChatDBGUIDsToExistingPortalUsesPopulatedContactAlias(t *testing.T) {
	contact := contactLookupForTests(&imessage.Contact{
		FirstName: "Person",
		Phones:    []string{"+15550000003"},
		Emails:    []string{"person@example.com"},
	})
	existing := map[string]existingDMPortalCandidate{
		"tel:+15550000003":          {ID: "tel:+15550000003"},
		"mailto:person@example.com": {ID: "mailto:person@example.com", HasMessages: true},
	}
	got := matchChatDBGUIDsToExistingPortals(
		[]chatDBGUIDRefreshEntry{
			{PortalID: "tel:+15550000003", ChatGUID: "SMS;-;+15550000003(smsft)"},
			{PortalID: "mailto:person@example.com", ChatGUID: "iMessage;-;person@example.com"},
		},
		contact,
		nil,
		func(portalID string) existingDMPortalCandidate { return existing[portalID] },
	)
	want := map[string][]string{
		"mailto:person@example.com": {
			"SMS;-;+15550000003(smsft)",
			"iMessage;-;person@example.com",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contact alias GUID assignments = %#v, want %#v", got, want)
	}
}

func TestApplyChatDBGUIDMetadataRefreshUnionsRetriesAndIsIdempotent(t *testing.T) {
	oldGUID := "SMS;-;+15550000004(sms)"
	newGUID := "SMS;-;+15550000004(smsft)"
	originalMetadata := &PortalMetadata{
		ThreadID:    "preserved-thread",
		ChatDBGUIDs: []string{oldGUID},
	}
	portal := &bridgev2.Portal{Portal: &database.Portal{Metadata: originalMetadata}}
	portals := map[string]*bridgev2.Portal{"tel:+15550000004": portal}
	assignments := map[string][]string{
		"tel:+15550000004": {newGUID},
	}

	saveCalls := 0
	failingSave := func(context.Context, *bridgev2.Portal) error {
		saveCalls++
		return errors.New("temporary database failure")
	}
	updated, unchanged, err := applyChatDBGUIDMetadataRefresh(context.Background(), assignments, portals, failingSave)
	if err == nil {
		t.Fatal("failed metadata save returned no error")
	}
	if updated != 0 || unchanged != 0 || saveCalls != 1 {
		t.Fatalf("failed pass = updated %d, unchanged %d, saves %d; want 0, 0, 1", updated, unchanged, saveCalls)
	}
	if portal.Metadata != originalMetadata {
		t.Fatal("failed save did not restore the original in-memory metadata object")
	}
	if !reflect.DeepEqual(originalMetadata.ChatDBGUIDs, []string{oldGUID}) {
		t.Fatalf("failed save mutated original GUIDs: %#v", originalMetadata.ChatDBGUIDs)
	}

	successfulSave := func(context.Context, *bridgev2.Portal) error {
		saveCalls++
		return nil
	}
	updated, unchanged, err = applyChatDBGUIDMetadataRefresh(context.Background(), assignments, portals, successfulSave)
	if err != nil {
		t.Fatalf("retry metadata refresh failed: %v", err)
	}
	if updated != 1 || unchanged != 0 || saveCalls != 2 {
		t.Fatalf("retry pass = updated %d, unchanged %d, saves %d; want 1, 0, 2", updated, unchanged, saveCalls)
	}
	meta := portal.Metadata.(*PortalMetadata)
	if meta.ThreadID != "preserved-thread" || !reflect.DeepEqual(meta.ChatDBGUIDs, []string{oldGUID, newGUID}) {
		t.Fatalf("successful retry metadata = %#v", meta)
	}

	updated, unchanged, err = applyChatDBGUIDMetadataRefresh(context.Background(), assignments, portals, successfulSave)
	if err != nil {
		t.Fatalf("idempotent metadata refresh failed: %v", err)
	}
	if updated != 0 || unchanged != 1 || saveCalls != 2 {
		t.Fatalf("idempotent pass = updated %d, unchanged %d, saves %d; want 0, 1, 2", updated, unchanged, saveCalls)
	}
}

func TestApplyChatDBGUIDMetadataRefreshDoesNotCreatePortals(t *testing.T) {
	updated, unchanged, err := applyChatDBGUIDMetadataRefresh(
		context.Background(),
		map[string][]string{"tel:+15550000005": {"SMS;-;+15550000005(smsft)"}},
		map[string]*bridgev2.Portal{},
		func(context.Context, *bridgev2.Portal) error {
			t.Fatal("save called for a nonexistent portal")
			return nil
		},
	)
	if err != nil || updated != 0 || unchanged != 0 {
		t.Fatalf("nonexistent portal refresh = updated %d unchanged %d err %v, want no-op", updated, unchanged, err)
	}
}
