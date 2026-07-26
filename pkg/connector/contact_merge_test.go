package connector

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/lrhodin/corten-matrix/imessage"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func contactLookupForTests(contacts ...*imessage.Contact) func(string) *imessage.Contact {
	byPortalID := make(map[string]*imessage.Contact)
	for _, contact := range contacts {
		for _, portalID := range contactPortalIDs(contact) {
			byPortalID[portalID] = contact
		}
	}
	return func(portalID string) *imessage.Contact {
		return byPortalID[portalID]
	}
}

func TestContactPortalIDsNormalizesDedupesAndSkipsBlankEmails(t *testing.T) {
	contact := &imessage.Contact{
		Phones: []string{
			"(555) 123-4567",
			"+1 555 123 4567",
			"555.765.4321",
		},
		Emails: []string{
			" USER@example.COM ",
			"",
			"user@example.com",
			"other@example.com",
		},
	}

	got := contactPortalIDs(contact)
	want := []string{
		"tel:+15551234567",
		"tel:+15557654321",
		"mailto:user@example.com",
		"mailto:other@example.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contactPortalIDs() = %#v, want %#v", got, want)
	}
}

func TestContactPortalIDsHandlesNilContact(t *testing.T) {
	if got := contactPortalIDs(nil); got != nil {
		t.Fatalf("contactPortalIDs(nil) = %#v, want nil", got)
	}
}

func TestChooseContactPortalIDUsesStableFreshIdentityAndFailsClosed(t *testing.T) {
	contact := &imessage.Contact{
		FirstName: "Fresh",
		Phones:    []string{"+15550000002", "+15550000001"},
		Emails:    []string{"fresh@example.com"},
	}
	incoming := "mailto:fresh@example.com"
	got, err := chooseContactPortalID(
		incoming,
		contact,
		nil,
		func(candidates []string) (existingDMPortalCandidate, error) {
			want := []string{"tel:+15550000001", "tel:+15550000002", incoming}
			if !reflect.DeepEqual(candidates, want) {
				t.Fatalf("candidate order = %#v, want %#v", candidates, want)
			}
			return existingDMPortalCandidate{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "tel:+15550000001" {
		t.Fatalf("fresh live portal ID = %q, want deterministic preferred phone", got)
	}

	lookupErr := errors.New("temporary portal lookup failure")
	got, err = chooseContactPortalID(
		incoming,
		contact,
		nil,
		func([]string) (existingDMPortalCandidate, error) {
			return existingDMPortalCandidate{}, lookupErr
		},
	)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("lookup error = %v, want %v", err, lookupErr)
	}
	if got != networkid.PortalID(incoming) {
		t.Fatalf("failed lookup portal ID = %q, want fail-closed incoming ID", got)
	}
}

func TestChooseContactPortalIDConcurrentFirstAliasesConverge(t *testing.T) {
	contact := &imessage.Contact{
		FirstName: "Concurrent",
		Phones:    []string{"+15550000002"},
		Emails:    []string{"concurrent@example.com"},
	}
	incoming := []string{"tel:+15550000002", "mailto:concurrent@example.com"}
	results := make(chan networkid.PortalID, len(incoming))
	var wg sync.WaitGroup
	for _, identifier := range incoming {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chosen, err := chooseContactPortalID(
				identifier,
				contact,
				nil,
				func([]string) (existingDMPortalCandidate, error) {
					return existingDMPortalCandidate{}, nil
				},
			)
			if err != nil {
				t.Errorf("chooseContactPortalID(%q): %v", identifier, err)
				return
			}
			results <- chosen
		}()
	}
	wg.Wait()
	close(results)
	for chosen := range results {
		if chosen != "tel:+15550000002" {
			t.Fatalf("concurrent first alias chose %q, want stable phone ID", chosen)
		}
	}
}

func TestLiveContactPortalIDCandidatesExcludeSelfAliasesButPreserveExistingSpelling(t *testing.T) {
	const (
		self       = "tel:+15559999999"
		selfLegacy = "tel:5559999999"
		peer       = "mailto:peer@example.com"
		peerLegacy = "MAILTO:peer@example.com"
	)
	contact := &imessage.Contact{
		FirstName: "Mixed",
		Phones:    []string{"+15559999999"},
		Emails:    []string{"peer@example.com"},
	}
	isSelf := func(identifier string) bool {
		return normalizeIdentifierForPortalID(identifier) == self
	}
	if !contactHasSelfPortalID(contact, isSelf) {
		t.Fatal("mixed contact was not identified as containing a self handle")
	}

	t.Run("self handle", func(t *testing.T) {
		got, err := chooseContactPortalID(
			self,
			contact,
			isSelf,
			func(candidates []string) (existingDMPortalCandidate, error) {
				if want := []string{self}; !reflect.DeepEqual(candidates, want) {
					t.Fatalf("self candidates = %#v, want only incoming %#v", candidates, want)
				}
				return existingDMPortalCandidate{ID: selfLegacy, HasMessages: true}, nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if got != selfLegacy {
			t.Fatalf("self portal ID = %q, want existing legacy spelling %q", got, selfLegacy)
		}
	})

	t.Run("peer sharing self contact", func(t *testing.T) {
		got, err := chooseExistingDMPortalID(
			peer,
			contact,
			isSelf,
			func(candidates []string) (existingDMPortalCandidate, error) {
				if want := []string{peer}; !reflect.DeepEqual(candidates, want) {
					t.Fatalf("mixed-contact candidates = %#v, want only incoming %#v", candidates, want)
				}
				return existingDMPortalCandidate{ID: peerLegacy, HasMessages: true}, nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if got != peerLegacy {
			t.Fatalf("peer portal ID = %q, want existing exact spelling %q", got, peerLegacy)
		}

		got, err = chooseContactPortalID(
			peer,
			contact,
			isSelf,
			func(candidates []string) (existingDMPortalCandidate, error) {
				if want := []string{peer}; !reflect.DeepEqual(candidates, want) {
					t.Fatalf("fresh mixed-contact candidates = %#v, want only incoming %#v", candidates, want)
				}
				return existingDMPortalCandidate{}, nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if got != peer {
			t.Fatalf("fresh peer portal ID = %q, want incoming %q", got, peer)
		}
	})
}

func TestPreferredExistingDMPortalCandidatePrefersPopulatedRoom(t *testing.T) {
	candidates := []string{"tel:+15550000001", "mailto:person@example.com"}
	existing := map[string]existingDMPortalCandidate{
		"tel:+15550000001":          {ID: "tel:+15550000001"},
		"mailto:person@example.com": {ID: "mailto:person@example.com", HasMessages: true},
	}
	got := preferredExistingDMPortalCandidate(candidates, func(candidate string) existingDMPortalCandidate {
		return existing[candidate]
	})
	want := existingDMPortalCandidate{ID: "mailto:person@example.com", HasMessages: true}
	if got != want {
		t.Fatalf("preferred existing portal = %#v, want populated room %#v", got, want)
	}
}

func TestCanonicalizeChatDBInitialSyncDMPortalIDs(t *testing.T) {
	tests := []struct {
		name         string
		contacts     []*imessage.Contact
		portalIDs    []string
		existingRoom map[string]existingDMPortalCandidate
		selfIDs      map[string]bool
		wantIDs      []string
		wantSkip     map[int]bool
	}{
		{
			name: "phone and email combine under phone",
			contacts: []*imessage.Contact{{
				FirstName: "PhoneEmail",
				Phones:    []string{"+15550000002"},
				Emails:    []string{"person@example.com"},
			}},
			portalIDs: []string{"mailto:person@example.com", "tel:+15550000002"},
			wantIDs:   []string{"tel:+15550000002", "tel:+15550000002"},
			wantSkip:  map[int]bool{1: true},
		},
		{
			name: "single email chat still canonicalizes to phone",
			contacts: []*imessage.Contact{{
				FirstName: "SingleAlias",
				Phones:    []string{"+15550000012"},
				Emails:    []string{"single@example.com"},
			}},
			portalIDs: []string{"mailto:single@example.com"},
			wantIDs:   []string{"tel:+15550000012"},
			wantSkip:  map[int]bool{},
		},
		{
			name: "multiple emails combine deterministically",
			contacts: []*imessage.Contact{{
				FirstName: "Emails",
				Emails:    []string{"zeta@example.com", "alpha@example.com"},
			}},
			portalIDs: []string{"mailto:zeta@example.com", "mailto:alpha@example.com"},
			wantIDs:   []string{"mailto:alpha@example.com", "mailto:alpha@example.com"},
			wantSkip:  map[int]bool{1: true},
		},
		{
			name: "multiple phones and emails prefer sorted phone",
			contacts: []*imessage.Contact{{
				FirstName: "ManyHandles",
				Phones:    []string{"+15550000009", "+15550000001"},
				Emails:    []string{"person@example.com", "other@example.com"},
			}},
			portalIDs: []string{
				"mailto:person@example.com",
				"tel:+15550000009",
				"mailto:other@example.com",
				"tel:+15550000001",
			},
			wantIDs: []string{
				"tel:+15550000001",
				"tel:+15550000001",
				"tel:+15550000001",
				"tel:+15550000001",
			},
			wantSkip: map[int]bool{1: true, 2: true, 3: true},
		},
		{
			name: "existing noncanonical portal is preserved",
			contacts: []*imessage.Contact{{
				FirstName: "Existing",
				Phones:    []string{"+15550000003"},
				Emails:    []string{"existing@example.com"},
			}},
			portalIDs: []string{"tel:+15550000003", "mailto:existing@example.com"},
			existingRoom: map[string]existingDMPortalCandidate{
				"mailto:existing@example.com": {ID: "mailto:existing@example.com"},
			},
			wantIDs:  []string{"mailto:existing@example.com", "mailto:existing@example.com"},
			wantSkip: map[int]bool{1: true},
		},
		{
			name: "populated alias beats empty preferred alias",
			contacts: []*imessage.Contact{{
				FirstName: "ExistingPopulated",
				Phones:    []string{"+15550000014"},
				Emails:    []string{"populated@example.com"},
			}},
			portalIDs: []string{"tel:+15550000014", "mailto:populated@example.com"},
			existingRoom: map[string]existingDMPortalCandidate{
				"tel:+15550000014":             {ID: "tel:+15550000014"},
				"mailto:populated@example.com": {ID: "mailto:populated@example.com", HasMessages: true},
			},
			wantIDs:  []string{"mailto:populated@example.com", "mailto:populated@example.com"},
			wantSkip: map[int]bool{1: true},
		},
		{
			name: "existing legacy phone portal keeps exact key",
			contacts: []*imessage.Contact{{
				FirstName: "LegacyPhone",
				Phones:    []string{"+15550000013"},
				Emails:    []string{"legacy@example.com"},
			}},
			portalIDs: []string{"mailto:legacy@example.com"},
			existingRoom: map[string]existingDMPortalCandidate{
				"tel:+15550000013": {ID: "tel:15550000013"},
			},
			wantIDs:  []string{"tel:15550000013"},
			wantSkip: map[int]bool{},
		},
		{
			name: "existing mixed case email portal keeps exact key",
			contacts: []*imessage.Contact{{
				FirstName: "LegacyEmail",
				Emails:    []string{"Person@Example.com", "other@example.com"},
			}},
			portalIDs: []string{"mailto:other@example.com"},
			existingRoom: map[string]existingDMPortalCandidate{
				"mailto:person@example.com": {ID: "mailto:Person@Example.com"},
			},
			wantIDs:  []string{"mailto:Person@Example.com"},
			wantSkip: map[int]bool{},
		},
		{
			name: "self contact is never canonicalized to another handle",
			contacts: []*imessage.Contact{{
				FirstName: "Self",
				Phones:    []string{"+15550000001", "+15559999999"},
			}},
			portalIDs: []string{"tel:+15559999999"},
			selfIDs:   map[string]bool{"tel:+15559999999": true},
			wantIDs:   []string{"tel:+15559999999"},
			wantSkip:  map[int]bool{},
		},
		{
			name: "unrelated contacts stay separate",
			contacts: []*imessage.Contact{
				{
					FirstName: "First",
					Phones:    []string{"+15550000004"},
					Emails:    []string{"first@example.com"},
				},
				{
					FirstName: "Second",
					Phones:    []string{"+15550000005"},
					Emails:    []string{"second@example.com"},
				},
			},
			portalIDs: []string{
				"mailto:first@example.com",
				"tel:+15550000004",
				"mailto:second@example.com",
				"tel:+15550000005",
			},
			wantIDs: []string{
				"tel:+15550000004",
				"tel:+15550000004",
				"tel:+15550000005",
				"tel:+15550000005",
			},
			wantSkip: map[int]bool{1: true, 3: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findExistingRoom := func(portalID string) existingDMPortalCandidate {
				return tt.existingRoom[portalID]
			}
			isSelf := func(portalID string) bool { return tt.selfIDs[portalID] }
			gotIDs, gotSkip := canonicalizeChatDBInitialSyncDMPortalIDs(
				tt.portalIDs,
				contactLookupForTests(tt.contacts...),
				isSelf,
				findExistingRoom,
			)
			if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Fatalf("canonical portal IDs = %#v, want %#v", gotIDs, tt.wantIDs)
			}
			if !reflect.DeepEqual(gotSkip, tt.wantSkip) {
				t.Fatalf("skip map = %#v, want %#v", gotSkip, tt.wantSkip)
			}
			if got, want := len(gotIDs)-len(gotSkip), len(tt.wantIDs)-len(tt.wantSkip); got != want {
				t.Fatalf("combined backfill entries = %d, want %d", got, want)
			}
		})
	}
}

func TestCanonicalizeChatDBInitialSyncPortalIndexFailureIsRetryable(t *testing.T) {
	const receiver = networkid.UserLoginID("login")
	contact := &imessage.Contact{
		FirstName: "Retry",
		Phones:    []string{"+15550000003"},
		Emails:    []string{"existing@example.com"},
	}
	portalIDs := []string{"tel:+15550000003", "mailto:existing@example.com"}
	loadErr := errors.New("temporary portal index failure")
	attempts := 0
	loadExistingRooms := func(context.Context) ([]*bridgev2.Portal, error) {
		attempts++
		if attempts == 1 {
			return nil, loadErr
		}
		return []*bridgev2.Portal{{
			Portal: &database.Portal{PortalKey: networkid.PortalKey{
				ID:       "mailto:existing@example.com",
				Receiver: receiver,
			}},
		}}, nil
	}

	canonical, skip, err := canonicalizeChatDBInitialSyncDMPortalIDsWithExistingRooms(
		context.Background(),
		portalIDs,
		receiver,
		contactLookupForTests(contact),
		nil,
		loadExistingRooms,
		nil,
	)
	if !errors.Is(err, loadErr) || canonical != nil || skip != nil {
		t.Fatalf("failed attempt = canonical %#v skip %#v err %v, want no result and %v", canonical, skip, err, loadErr)
	}

	canonical, skip, err = canonicalizeChatDBInitialSyncDMPortalIDsWithExistingRooms(
		context.Background(),
		portalIDs,
		receiver,
		contactLookupForTests(contact),
		nil,
		loadExistingRooms,
		nil,
	)
	if err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	wantCanonical := []string{"mailto:existing@example.com", "mailto:existing@example.com"}
	if !reflect.DeepEqual(canonical, wantCanonical) || !reflect.DeepEqual(skip, map[int]bool{1: true}) {
		t.Fatalf("retry = canonical %#v skip %#v, want %#v and second skipped", canonical, skip, wantCanonical)
	}
}

func TestCanonicalizeChatDBInitialSyncInspectionChoosesPopulatedAndRetriesErrors(t *testing.T) {
	const receiver = networkid.UserLoginID("login")
	contact := &imessage.Contact{
		FirstName: "Inspect",
		Phones:    []string{"+15550000014"},
		Emails:    []string{"populated@example.com"},
	}
	portalIDs := []string{"tel:+15550000014", "mailto:populated@example.com"}
	existing := []*bridgev2.Portal{
		{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "tel:+15550000014", Receiver: receiver}}},
		{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "mailto:populated@example.com", Receiver: receiver}}},
	}
	inspectErr := errors.New("temporary message inspection failure")
	failInspection := true
	inspectionCount := make(map[string]int)
	inspect := func(portal *bridgev2.Portal) (existingDMPortalCandidate, error) {
		portalID := string(portal.ID)
		inspectionCount[portalID]++
		if failInspection {
			return existingDMPortalCandidate{}, inspectErr
		}
		return existingDMPortalCandidate{
			ID:          portalID,
			HasMessages: portalID == "mailto:populated@example.com",
		}, nil
	}
	load := func(context.Context) ([]*bridgev2.Portal, error) { return existing, nil }

	canonical, skip, err := canonicalizeChatDBInitialSyncDMPortalIDsWithExistingRooms(
		context.Background(), portalIDs, receiver, contactLookupForTests(contact), nil, load, inspect,
	)
	if !errors.Is(err, inspectErr) || canonical != nil || skip != nil {
		t.Fatalf("inspection failure = canonical %#v skip %#v err %v", canonical, skip, err)
	}

	failInspection = false
	inspectionCount = make(map[string]int)
	canonical, skip, err = canonicalizeChatDBInitialSyncDMPortalIDsWithExistingRooms(
		context.Background(), portalIDs, receiver, contactLookupForTests(contact), nil, load, inspect,
	)
	if err != nil {
		t.Fatalf("inspection retry returned error: %v", err)
	}
	wantCanonical := []string{"mailto:populated@example.com", "mailto:populated@example.com"}
	if !reflect.DeepEqual(canonical, wantCanonical) || !reflect.DeepEqual(skip, map[int]bool{1: true}) {
		t.Fatalf("inspection retry = canonical %#v skip %#v, want populated alias", canonical, skip)
	}
	for _, portalID := range portalIDs {
		if inspectionCount[portalID] != 1 {
			t.Fatalf("inspected %q %d times, want once", portalID, inspectionCount[portalID])
		}
	}
}

func TestExistingDMPortalIDVariantsPreserveExactAndLegacyForms(t *testing.T) {
	tests := []struct {
		identifier string
		want       []string
	}{
		{
			identifier: "mailto:Person@Example.com",
			want:       []string{"mailto:Person@Example.com", "mailto:person@example.com"},
		},
		{
			identifier: "tel:+15550000013",
			want:       []string{"tel:+15550000013", "tel:15550000013", "tel:5550000013"},
		},
	}
	for _, tt := range tests {
		if got := existingDMPortalIDVariants(tt.identifier); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("existingDMPortalIDVariants(%q) = %#v, want %#v", tt.identifier, got, tt.want)
		}
	}
}

func TestChatDBInfoToBridgev2UsesCanonicalDMIdentity(t *testing.T) {
	client := &IMClient{
		handle: "tel:+15559999999",
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			ID: networkid.UserLoginID("login"),
		}},
	}
	info := &imessage.ChatInfo{JSONChatGUID: "iMessage;-;alias@example.com"}
	canonicalPortalID := networkid.PortalID("tel:+15550000002")

	got := client.chatDBInfoToBridgev2(info, canonicalPortalID)
	wantUserID := makeUserID(string(canonicalPortalID))
	if got.Members == nil {
		t.Fatal("chatDBInfoToBridgev2 returned no DM members")
	}
	if got.Members.OtherUserID != wantUserID {
		t.Fatalf("DM OtherUserID = %q, want %q", got.Members.OtherUserID, wantUserID)
	}
	if _, ok := got.Members.MemberMap[wantUserID]; !ok {
		t.Fatalf("canonical DM user %q missing from member map %#v", wantUserID, got.Members.MemberMap)
	}
	aliasUserID := makeUserID("mailto:alias@example.com")
	if _, ok := got.Members.MemberMap[aliasUserID]; ok {
		t.Fatalf("noncanonical alias %q unexpectedly present in member map %#v", aliasUserID, got.Members.MemberMap)
	}
}

func TestInitialSyncMixedAliasesUseRetainedRepresentativeSMSState(t *testing.T) {
	contact := &imessage.Contact{
		FirstName: "MixedService",
		Phones:    []string{"+15550000021"},
		Emails:    []string{"mixed@example.com"},
	}
	portalIDs := []string{"mailto:mixed@example.com", "tel:+15550000021"}
	canonical, skip := canonicalizeChatDBInitialSyncDMPortalIDs(
		portalIDs, contactLookupForTests(contact), nil, nil,
	)
	if canonical[0] != "tel:+15550000021" || canonical[1] != "tel:+15550000021" {
		t.Fatalf("canonical portal IDs = %#v", canonical)
	}
	if !skip[1] {
		t.Fatalf("older SMS alias was not discarded: %#v", skip)
	}

	// The older discarded alias previously marked the canonical portal SMS.
	// Applying the newer retained iMessage representative must clear both the
	// flag and its stale SMS destination.
	meta, changed := portalMetadataWithSMSRouting(
		&PortalMetadata{IsSms: true, SMSDestination: "tel:+15550000021"},
		false,
		"",
	)
	if !changed {
		t.Fatal("retained iMessage representative did not change stale SMS metadata")
	}
	if meta.IsSms || meta.SMSDestination != "" {
		t.Fatalf("retained iMessage metadata = %+v, want non-SMS with no destination", meta)
	}
}

func TestSMSDestinationForDMUsesRemoteEnvelopeHandle(t *testing.T) {
	const self = "tel:+15559999999"
	client := &IMClient{
		handle:     self,
		allHandles: []string{self, "mailto:self@example.com"},
	}
	sender := "+1 (555) 000-0023"
	if got := client.smsDestinationForDM([]string{self}, &sender); got != "tel:+15550000023" {
		t.Fatalf("sender fallback destination = %q, want tel:+15550000023", got)
	}
	if got := client.smsDestinationForDM(
		[]string{"mailto:self@example.com", "5550000024"},
		&sender,
	); got != "tel:+15550000024" {
		t.Fatalf("participant destination = %q, want tel:+15550000024", got)
	}
}

func TestPortalMetadataWithSMSRoutingUpdatesDestinationWithoutServiceChange(t *testing.T) {
	existing := &PortalMetadata{
		ThreadID:       "thread",
		IsSms:          true,
		SMSDestination: "tel:+15550000021",
	}
	meta, changed := portalMetadataWithSMSRouting(existing, true, "tel:+15550000022")
	if !changed {
		t.Fatal("destination-only change was not detected")
	}
	if meta.SMSDestination != "tel:+15550000022" || !meta.IsSms {
		t.Fatalf("updated metadata = %+v", meta)
	}
	if meta.ThreadID != existing.ThreadID {
		t.Fatalf("unrelated metadata was not preserved: %+v", meta)
	}
	if existing.SMSDestination != "tel:+15550000021" {
		t.Fatalf("input metadata was mutated: %+v", existing)
	}
}

func TestPersistPortalSMSRoutingPropagatesSaveFailureForRetry(t *testing.T) {
	saveErr := errors.New("save failed")
	existing := &PortalMetadata{
		ThreadID:       "thread",
		IsSms:          true,
		SMSDestination: "tel:+15550000021",
	}
	portal := &bridgev2.Portal{Portal: &database.Portal{Metadata: existing}}
	attempts := 0
	changed, err := persistPortalSMSRouting(
		portal,
		true,
		"tel:+15550000022",
		func() error {
			attempts++
			return saveErr
		},
	)
	if !errors.Is(err, saveErr) {
		t.Fatalf("save error = %v, want %v", err, saveErr)
	}
	if !changed {
		t.Fatal("failed routing metadata write was not reported as a change")
	}
	if portal.Metadata != existing {
		t.Fatalf("portal metadata was not restored after failed save: got %+v, want original pointer %+v", portal.Metadata, existing)
	}

	changed, err = persistPortalSMSRouting(
		portal,
		true,
		"tel:+15550000022",
		func() error {
			attempts++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("retry save error = %v", err)
	}
	if !changed {
		t.Fatal("retry skipped routing metadata write after prior failure")
	}
	meta, ok := portal.Metadata.(*PortalMetadata)
	if !ok || meta.SMSDestination != "tel:+15550000022" {
		t.Fatalf("retry metadata = %+v, want updated destination", portal.Metadata)
	}
	if attempts != 2 {
		t.Fatalf("save attempts = %d, want 2", attempts)
	}
}

func TestSMSRoutingSaveFailureKeepsLatestRuntimeSnapshot(t *testing.T) {
	const (
		canonical = "mailto:mixed@example.com"
		self      = "tel:+15559999999"
		oldSMS    = "tel:+15550000021"
		newSMS    = "tel:+15550000022"
	)
	tests := []struct {
		name             string
		initialSMS       bool
		initialDest      string
		nextSMS          bool
		nextDest         string
		wantParticipants []string
	}{
		{
			name:             "iMessage to SMS",
			nextSMS:          true,
			nextDest:         newSMS,
			wantParticipants: []string{self, newSMS},
		},
		{
			name:             "SMS destination changes",
			initialSMS:       true,
			initialDest:      oldSMS,
			nextSMS:          true,
			nextDest:         newSMS,
			wantParticipants: []string{self, newSMS},
		},
		{
			name:             "SMS to iMessage",
			initialSMS:       true,
			initialDest:      oldSMS,
			wantParticipants: []string{self, canonical},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &IMClient{
				handle:          self,
				allHandles:      []string{self},
				smsPortals:      map[string]bool{canonical: tt.initialSMS},
				smsDestinations: map[string]string{canonical: tt.initialDest},
			}
			originalMetadata := &PortalMetadata{
				ThreadID:       "keep",
				IsSms:          tt.initialSMS,
				SMSDestination: tt.initialDest,
			}
			portal := &bridgev2.Portal{Portal: &database.Portal{
				PortalKey: networkid.PortalKey{ID: networkid.PortalID(canonical)},
				Metadata:  originalMetadata,
			}}

			client.updatePortalSMSRouting(canonical, tt.nextSMS, tt.nextDest)
			saveErr := errors.New("temporary save failure")
			changed, err := persistPortalSMSRouting(
				portal,
				tt.nextSMS,
				tt.nextDest,
				func() error { return saveErr },
			)
			if !errors.Is(err, saveErr) || !changed {
				t.Fatalf("failed save = changed %v err %v, want changed and %v", changed, err, saveErr)
			}
			if portal.Metadata != originalMetadata {
				t.Fatal("failed save did not restore persisted metadata")
			}

			conv := client.portalToConversation(portal)
			if conv.IsSms != tt.nextSMS {
				t.Fatalf("outbound IsSms after failure = %v, want %v", conv.IsSms, tt.nextSMS)
			}
			if !reflect.DeepEqual(conv.Participants, tt.wantParticipants) {
				t.Fatalf("outbound participants after failure = %#v, want %#v", conv.Participants, tt.wantParticipants)
			}
		})
	}
}

func TestUpdateAndPersistPortalSMSRoutingSerializesSaveRollback(t *testing.T) {
	const (
		portalID = "mailto:mixed@example.com"
		firstSMS = "tel:+15550000021"
		nextSMS  = "tel:+15550000022"
	)
	client := &IMClient{smsPortals: make(map[string]bool)}
	originalMetadata := &PortalMetadata{ThreadID: "keep"}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: networkid.PortalID(portalID)},
		Metadata:  originalMetadata,
	}}

	firstSaveStarted := make(chan struct{})
	allowFirstFailure := make(chan struct{})
	firstResult := make(chan error, 1)
	saveErr := errors.New("first save failed")
	go func() {
		_, err := client.updateAndPersistPortalSMSRouting(
			portalID,
			portal,
			true,
			firstSMS,
			func() error {
				close(firstSaveStarted)
				<-allowFirstFailure
				return saveErr
			},
		)
		firstResult <- err
	}()
	<-firstSaveStarted

	if client.smsRoutePersistMu.TryLock() {
		client.smsRoutePersistMu.Unlock()
		t.Fatal("metadata persistence mutex was not held across Save")
	}
	if got := client.getPortalSMSRouting(portalID, nil); got != (portalSMSRouting{IsSMS: true, Destination: firstSMS}) {
		t.Fatalf("runtime route during first Save = %+v", got)
	}

	secondCallStarted := make(chan struct{})
	secondSaveStarted := make(chan struct{})
	secondResult := make(chan error, 1)
	go func() {
		close(secondCallStarted)
		_, err := client.updateAndPersistPortalSMSRouting(
			portalID,
			portal,
			true,
			nextSMS,
			func() error {
				close(secondSaveStarted)
				return nil
			},
		)
		secondResult <- err
	}()
	<-secondCallStarted
	close(allowFirstFailure)

	if err := <-firstResult; !errors.Is(err, saveErr) {
		t.Fatalf("first transaction error = %v, want %v", err, saveErr)
	}
	<-secondSaveStarted
	if err := <-secondResult; err != nil {
		t.Fatalf("second transaction error = %v", err)
	}

	meta, ok := portal.Metadata.(*PortalMetadata)
	if !ok || meta.SMSDestination != nextSMS || !meta.IsSms || meta.ThreadID != "keep" {
		t.Fatalf("final metadata = %+v, want successful second route with preserved fields", portal.Metadata)
	}
	if got := client.getPortalSMSRouting(portalID, nil); got != (portalSMSRouting{IsSMS: true, Destination: nextSMS}) {
		t.Fatalf("final runtime route = %+v", got)
	}

	const missingPortalID = "tel:+15550000023"
	changed, err := client.updateAndPersistPortalSMSRouting(
		missingPortalID, nil, true, missingPortalID, nil,
	)
	if err != nil || changed {
		t.Fatalf("missing portal transaction = changed %v err %v, want runtime-only success", changed, err)
	}
	if got := client.getPortalSMSRouting(missingPortalID, nil); got != (portalSMSRouting{IsSMS: true, Destination: missingPortalID}) {
		t.Fatalf("missing portal runtime route = %+v", got)
	}
	createdPortal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: networkid.PortalID(missingPortalID)},
	}}
	saves := 0
	changed, err = client.persistCurrentPortalSMSRouting(
		missingPortalID,
		createdPortal,
		func() error {
			saves++
			return nil
		},
	)
	if err != nil || !changed || saves != 1 {
		t.Fatalf("created portal persistence = changed %v saves %d err %v", changed, saves, err)
	}
	if meta, ok := createdPortal.Metadata.(*PortalMetadata); !ok || !meta.IsSms || meta.SMSDestination != missingPortalID {
		t.Fatalf("created portal metadata = %+v", createdPortal.Metadata)
	}
}

func TestInitialSyncRouteSeedPreservesNewerLiveRouteBeforePostHandle(t *testing.T) {
	const (
		portalID      = "mailto:mixed@example.com"
		chatDBSMSDest = "tel:+15550000021"
	)
	client := &IMClient{}
	if !client.seedPortalSMSRoutingIfAbsent(portalID, true, chatDBSMSDest) {
		t.Fatal("initial chat.db route was not seeded")
	}

	// A live iMessage transition arrives after the scan but before the initial
	// sync PostHandle persists portal metadata.
	if _, err := client.updateAndPersistPortalSMSRouting(
		portalID, nil, false, "", nil,
	); err != nil {
		t.Fatalf("live route update failed: %v", err)
	}
	if client.seedPortalSMSRoutingIfAbsent(portalID, true, chatDBSMSDest) {
		t.Fatal("chat.db seed overwrote an existing live route")
	}

	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: networkid.PortalID(portalID)},
		Metadata: &PortalMetadata{
			IsSms:          true,
			SMSDestination: chatDBSMSDest,
		},
	}}
	saves := 0
	changed, err := client.persistCurrentPortalSMSRouting(
		portalID,
		portal,
		func() error {
			saves++
			return nil
		},
	)
	if err != nil || !changed || saves != 1 {
		t.Fatalf("post-handle persistence = changed %v saves %d err %v", changed, saves, err)
	}
	if got := client.getPortalSMSRouting(portalID, nil); got != (portalSMSRouting{}) {
		t.Fatalf("runtime route = %+v, want live iMessage route", got)
	}
	meta, ok := portal.Metadata.(*PortalMetadata)
	if !ok || meta.IsSms || meta.SMSDestination != "" {
		t.Fatalf("persisted route = %+v, want live iMessage route", portal.Metadata)
	}
}

func TestPartialChatDBSyncSkipsStaleMetadataBeforeRouteSeed(t *testing.T) {
	const (
		portalID        = "mailto:mixed@example.com"
		staleSMSDest    = "tel:+15550000021"
		currentLiveDest = "tel:+15550000022"
	)
	if shouldHydratePersistedSMSRouting(true, false) {
		t.Fatal("partial chat.db sync unexpectedly hydrates persisted routing")
	}
	if !shouldHydratePersistedSMSRouting(true, true) {
		t.Fatal("completed chat.db sync did not hydrate persisted routing")
	}
	if !shouldHydratePersistedSMSRouting(false, false) {
		t.Fatal("non-chat.db mode did not hydrate persisted routing")
	}

	staleMetadata := &PortalMetadata{IsSms: true, SMSDestination: staleSMSDest}
	t.Run("no live route", func(t *testing.T) {
		client := &IMClient{}
		if shouldHydratePersistedSMSRouting(true, false) && staleMetadata.IsSms {
			client.updatePortalSMSRouting(portalID, true, staleMetadata.SMSDestination)
		}
		if !client.seedPortalSMSRoutingIfAbsent(portalID, false, "") {
			t.Fatal("current chat.db iMessage route was not seeded")
		}
		if got := client.getPortalSMSRouting(portalID, nil); got != (portalSMSRouting{}) {
			t.Fatalf("runtime route = %+v, want current chat.db iMessage route", got)
		}
	})

	t.Run("live before seed", func(t *testing.T) {
		client := &IMClient{}
		if shouldHydratePersistedSMSRouting(true, false) && staleMetadata.IsSms {
			client.updatePortalSMSRouting(portalID, true, staleMetadata.SMSDestination)
		}
		client.updatePortalSMSRouting(portalID, true, currentLiveDest)
		if client.seedPortalSMSRoutingIfAbsent(portalID, false, "") {
			t.Fatal("chat.db seed overwrote a live route")
		}
		want := portalSMSRouting{IsSMS: true, Destination: currentLiveDest}
		if got := client.getPortalSMSRouting(portalID, nil); got != want {
			t.Fatalf("runtime route = %+v, want live route %+v", got, want)
		}
	})
}

func TestPortalSMSRoutingConcurrentSnapshots(t *testing.T) {
	const portalID = "mailto:mixed@example.com"
	client := &IMClient{smsPortals: make(map[string]bool)}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan portalSMSRouting, 16)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 2000; i++ {
			if i%2 == 0 {
				client.updatePortalSMSRouting(portalID, true, "tel:+15550000022")
			} else {
				client.updatePortalSMSRouting(portalID, false, "")
			}
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 2000; i++ {
				routing := client.getPortalSMSRouting(portalID, nil)
				if (routing.IsSMS && routing.Destination != "tel:+15550000022") ||
					(!routing.IsSMS && routing.Destination != "") {
					select {
					case errs <- routing:
					default:
					}
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for routing := range errs {
		t.Fatalf("observed incoherent concurrent route: %+v", routing)
	}
}

func TestPortalToConversationUsesPersistedSMSDestination(t *testing.T) {
	const (
		canonical   = "mailto:mixed@example.com"
		destination = "tel:+15550000022"
		self        = "tel:+15559999999"
	)
	client := &IMClient{
		handle:     self,
		allHandles: []string{self},
	}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: networkid.PortalID(canonical)},
		Metadata: &PortalMetadata{
			IsSms:          true,
			SMSDestination: destination,
		},
	}}

	conv := client.portalToConversation(portal)
	if !conv.IsSms {
		t.Fatal("conversation is not marked SMS")
	}
	want := []string{self, destination}
	if !reflect.DeepEqual(conv.Participants, want) {
		t.Fatalf("SMS participants = %#v, want %#v", conv.Participants, want)
	}
}

func TestChatDBSelfAliasCanonicalizationPreservesDMIdentity(t *testing.T) {
	selfID := "tel:+15559999999"
	main := &IMConnector{Config: IMConfig{DisplaynameTemplate: "{{.ID}}"}}
	if err := main.Config.PostProcess(); err != nil {
		t.Fatalf("initialize displayname template: %v", err)
	}
	client := &IMClient{
		Main:       main,
		handle:     selfID,
		allHandles: []string{selfID},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			ID: networkid.UserLoginID("login"),
		}},
	}
	selfContact := &imessage.Contact{
		FirstName: "Self",
		Phones:    []string{"+15550000001", "+15559999999"},
	}
	portalIDs, skip := canonicalizeChatDBInitialSyncDMPortalIDs(
		[]string{selfID}, contactLookupForTests(selfContact), client.isMyHandle, nil,
	)
	if got := portalIDs[0]; got != selfID {
		t.Fatalf("self portal ID = %q, want %q", got, selfID)
	}
	if len(skip) != 0 {
		t.Fatalf("self chat unexpectedly skipped: %#v", skip)
	}

	info := &imessage.ChatInfo{JSONChatGUID: "iMessage;-;+15559999999"}
	chatInfo := client.chatDBInfoToBridgev2(info, networkid.PortalID(portalIDs[0]))
	wantUserID := makeUserID(selfID)
	if chatInfo.Members.OtherUserID != wantUserID {
		t.Fatalf("self DM OtherUserID = %q, want %q", chatInfo.Members.OtherUserID, wantUserID)
	}
	if len(chatInfo.Members.MemberMap) != 1 || !chatInfo.Members.MemberMap[wantUserID].IsFromMe {
		t.Fatalf("self DM member map = %#v, want one IsFromMe member", chatInfo.Members.MemberMap)
	}
}

func TestPickSendTargetPrimaryValid(t *testing.T) {
	portalID := "tel:+15551234567"
	altIDs := []string{"tel:+15557654321", "mailto:user@example.com"}
	validSet := map[string]struct{}{
		portalID:                  {},
		"tel:+15557654321":        {},
		"mailto:user@example.com": {},
	}

	got, ok := pickSendTarget(portalID, altIDs, validSet)
	if !ok || got != portalID {
		t.Fatalf("pickSendTarget() = (%q, %v), want (%q, true)", got, ok, portalID)
	}
}

func TestPickSendTargetFirstValidAlternate(t *testing.T) {
	portalID := "tel:+15551234567"
	altIDs := []string{"tel:+15557654321", "mailto:user@example.com"}
	validSet := map[string]struct{}{
		"mailto:user@example.com": {},
		"tel:+15557654321":        {},
	}

	got, ok := pickSendTarget(portalID, altIDs, validSet)
	if !ok || got != "tel:+15557654321" {
		t.Fatalf("pickSendTarget() = (%q, %v), want (%q, true)", got, ok, "tel:+15557654321")
	}
}

func TestPickSendTargetNothingValid(t *testing.T) {
	portalID := "tel:+15551234567"
	altIDs := []string{"tel:+15557654321", "mailto:user@example.com"}

	got, ok := pickSendTarget(portalID, altIDs, nil)
	if ok || got != portalID {
		t.Fatalf("pickSendTarget() = (%q, %v), want (%q, false)", got, ok, portalID)
	}
}

func TestValidateTargetsSafeGuardsNilClientAndEmptyTargets(t *testing.T) {
	c := &IMClient{}
	if got := c.validateTargetsSafe([]string{"tel:+15551234567"}); got != nil {
		t.Fatalf("validateTargetsSafe() with nil client = %#v, want nil", got)
	}
	if got := c.validateTargetsSafe(nil); got != nil {
		t.Fatalf("validateTargetsSafe(nil) = %#v, want nil", got)
	}
}
