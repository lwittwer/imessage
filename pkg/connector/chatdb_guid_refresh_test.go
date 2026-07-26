package connector

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lrhodin/corten-matrix/imessage"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type partiallyUnreadableChatDBRefreshAPI struct {
	imessage.API
}

func (partiallyUnreadableChatDBRefreshAPI) GetChatsWithMessagesAfter(time.Time) ([]imessage.ChatIdentifier, error) {
	return []imessage.ChatIdentifier{
		{ChatGUID: ""},
		{ChatGUID: "iMessage;+;chat-broken"},
		{ChatGUID: "SMS;-;+15550000006(smsft)"},
	}, nil
}

func (partiallyUnreadableChatDBRefreshAPI) GetChatInfo(chatID, _ string) (*imessage.ChatInfo, error) {
	if chatID == "iMessage;+;chat-broken" {
		return nil, errors.New("unreadable group row")
	}
	return nil, nil
}

func TestEnumerateChatDBGUIDRefreshEntriesSkipsUnreadableRows(t *testing.T) {
	client := &IMClient{chatDB: &chatDB{api: partiallyUnreadableChatDBRefreshAPI{}}}
	entries, err := client.enumerateChatDBGUIDRefreshEntries(context.Background())
	if err != nil {
		t.Fatalf("enumeration failed on per-row error: %v", err)
	}
	want := []chatDBGUIDRefreshEntry{{
		PortalID: "tel:+15550000006",
		ChatGUID: "SMS;-;+15550000006(smsft)",
	}}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("refresh entries = %#v, want %#v", entries, want)
	}
}

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

func TestPortalMetadataPersistenceSerializesExactGUIDAndLiveRoute(t *testing.T) {
	client := &IMClient{smsPortals: make(map[string]bool)}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: "tel:+15550000007"},
		Metadata:  &PortalMetadata{ThreadID: "keep"},
	}}
	exactSaveEntered := make(chan struct{})
	releaseExactSave := make(chan struct{})
	exactDone := make(chan error, 1)
	go func() {
		_, err := client.updatePortalChatDBGUIDMetadata(
			context.Background(),
			portal,
			[]string{"SMS;-;+15550000007(smsft)"},
			func(context.Context, *bridgev2.Portal) error {
				close(exactSaveEntered)
				<-releaseExactSave
				return nil
			},
		)
		exactDone <- err
	}()
	<-exactSaveEntered

	routeCallStarted := make(chan struct{})
	routeSaveEntered := make(chan struct{})
	routeDone := make(chan error, 1)
	go func() {
		close(routeCallStarted)
		_, err := client.persistPortalSMSRoutingWithSave(
			portal,
			true,
			"tel:+15550000007",
			func() error {
				close(routeSaveEntered)
				return nil
			},
		)
		routeDone <- err
	}()
	<-routeCallStarted
	select {
	case <-routeSaveEntered:
		t.Fatal("live route metadata save entered before exact-GUID transaction completed")
	case <-time.After(50 * time.Millisecond):
	}
	if client.isPortalSMS("tel:+15550000007") {
		t.Fatal("live runtime route updated before exact-GUID transaction completed")
	}

	close(releaseExactSave)
	if err := <-exactDone; err != nil {
		t.Fatalf("exact-GUID metadata save failed: %v", err)
	}
	if err := <-routeDone; err != nil {
		t.Fatalf("live route metadata save failed: %v", err)
	}
	if !client.isPortalSMS("tel:+15550000007") {
		t.Fatal("serialized live route did not update runtime routing")
	}
	meta := portal.Metadata.(*PortalMetadata)
	if meta.ThreadID != "keep" || !meta.IsSms || meta.SMSDestination != "tel:+15550000007" {
		t.Fatalf("serialized metadata lost route or unrelated state: %#v", meta)
	}
	wantGUIDs := []string{"SMS;-;+15550000007(smsft)"}
	if !reflect.DeepEqual(meta.ChatDBGUIDs, wantGUIDs) {
		t.Fatalf("serialized metadata GUIDs = %#v, want %#v", meta.ChatDBGUIDs, wantGUIDs)
	}
}

func TestPortalMetadataPersistencePreservesNewerLiveRouteBeforeExactGUIDSave(t *testing.T) {
	client := &IMClient{smsPortals: make(map[string]bool)}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: "tel:+15550000008"},
		Metadata: &PortalMetadata{
			IsSms:          true,
			SMSDestination: "tel:+15550000008",
		},
	}}
	routeSaveEntered := make(chan struct{})
	releaseRouteSave := make(chan struct{})
	routeDone := make(chan error, 1)
	go func() {
		_, err := client.persistPortalSMSRoutingWithSave(
			portal,
			false,
			"",
			func() error {
				close(routeSaveEntered)
				<-releaseRouteSave
				return nil
			},
		)
		routeDone <- err
	}()
	<-routeSaveEntered
	if client.isPortalSMS("tel:+15550000008") {
		t.Fatal("live iMessage route did not update runtime routing inside transaction")
	}

	exactCallStarted := make(chan struct{})
	exactSaveEntered := make(chan struct{})
	exactDone := make(chan error, 1)
	go func() {
		close(exactCallStarted)
		_, err := client.updatePortalChatDBGUIDMetadata(
			context.Background(),
			portal,
			[]string{"iMessage;-;+15550000008"},
			func(context.Context, *bridgev2.Portal) error {
				close(exactSaveEntered)
				return nil
			},
		)
		exactDone <- err
	}()
	<-exactCallStarted
	select {
	case <-exactSaveEntered:
		t.Fatal("exact-GUID metadata save entered before live route transaction completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseRouteSave)
	if err := <-routeDone; err != nil {
		t.Fatalf("live route metadata save failed: %v", err)
	}
	if err := <-exactDone; err != nil {
		t.Fatalf("exact-GUID metadata save failed: %v", err)
	}
	meta := portal.Metadata.(*PortalMetadata)
	if meta.IsSms || meta.SMSDestination != "" {
		t.Fatalf("exact-GUID save restored stale SMS route: %#v", meta)
	}
	wantGUIDs := []string{"iMessage;-;+15550000008"}
	if !reflect.DeepEqual(meta.ChatDBGUIDs, wantGUIDs) {
		t.Fatalf("serialized metadata GUIDs = %#v, want %#v", meta.ChatDBGUIDs, wantGUIDs)
	}
}

func TestExactGUIDSavePersistsRuntimeRouteAfterLiveRouteSaveFailure(t *testing.T) {
	const portalID = "tel:+15550000009"
	client := &IMClient{smsPortals: make(map[string]bool)}
	originalMetadata := &PortalMetadata{
		ThreadID:       "keep",
		IsSms:          true,
		SMSDestination: portalID,
	}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: portalID},
		Metadata:  originalMetadata,
	}}

	_, err := client.persistPortalSMSRoutingWithSave(
		portal,
		false,
		"",
		func() error { return errors.New("temporary route save failure") },
	)
	if err == nil {
		t.Fatal("failed live route save returned nil error")
	}
	if client.isPortalSMS(portalID) {
		t.Fatal("failed live save did not retain newer runtime iMessage route")
	}
	if portal.Metadata != originalMetadata {
		t.Fatal("failed live save did not roll portal metadata back")
	}

	exactGUID := "iMessage;-;+15550000009"
	_, err = client.updatePortalChatDBGUIDMetadata(
		context.Background(),
		portal,
		[]string{exactGUID},
		func(context.Context, *bridgev2.Portal) error { return nil },
	)
	if err != nil {
		t.Fatalf("exact-GUID save failed: %v", err)
	}
	meta := portal.Metadata.(*PortalMetadata)
	if meta.ThreadID != "keep" || meta.IsSms || meta.SMSDestination != "" {
		t.Fatalf("exact-GUID save did not persist current runtime route: %#v", meta)
	}
	if !reflect.DeepEqual(meta.ChatDBGUIDs, []string{exactGUID}) {
		t.Fatalf("exact-GUID save persisted GUIDs %#v, want %#v", meta.ChatDBGUIDs, []string{exactGUID})
	}
}
