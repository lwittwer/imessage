package connector

import (
	"context"
	"errors"
	"reflect"
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

func TestPreferredExistingDMPortalCandidate(t *testing.T) {
	candidates := []string{"tel:+15550000001", "mailto:person@example.com", "mailto:other@example.com"}
	tests := []struct {
		name     string
		existing map[string]existingDMPortalCandidate
		want     existingDMPortalCandidate
	}{
		{
			name: "populated alias beats empty preferred alias",
			existing: map[string]existingDMPortalCandidate{
				"tel:+15550000001":          {ID: "tel:+15550000001"},
				"mailto:person@example.com": {ID: "mailto:person@example.com", HasMessages: true},
			},
			want: existingDMPortalCandidate{ID: "mailto:person@example.com", HasMessages: true},
		},
		{
			name: "populated tie keeps preferred order",
			existing: map[string]existingDMPortalCandidate{
				"tel:+15550000001":          {ID: "tel:+15550000001", HasMessages: true},
				"mailto:person@example.com": {ID: "mailto:person@example.com", HasMessages: true},
			},
			want: existingDMPortalCandidate{ID: "tel:+15550000001", HasMessages: true},
		},
		{
			name: "empty tie keeps preferred order",
			existing: map[string]existingDMPortalCandidate{
				"tel:+15550000001":          {ID: "tel:+15550000001"},
				"mailto:person@example.com": {ID: "mailto:person@example.com"},
			},
			want: existingDMPortalCandidate{ID: "tel:+15550000001"},
		},
		{
			name: "no existing alias",
			want: existingDMPortalCandidate{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preferredExistingDMPortalCandidate(candidates, func(candidate string) existingDMPortalCandidate {
				return tt.existing[candidate]
			})
			if got != tt.want {
				t.Fatalf("preferred existing portal = %#v, want %#v", got, tt.want)
			}
		})
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
			name: "populated existing alias beats empty preferred alias",
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
			name: "equally populated aliases use preferred order",
			contacts: []*imessage.Contact{{
				FirstName: "ExistingTie",
				Phones:    []string{"+15550000015"},
				Emails:    []string{"tie@example.com"},
			}},
			portalIDs: []string{"mailto:tie@example.com", "tel:+15550000015"},
			existingRoom: map[string]existingDMPortalCandidate{
				"tel:+15550000015":       {ID: "tel:+15550000015", HasMessages: true},
				"mailto:tie@example.com": {ID: "mailto:tie@example.com", HasMessages: true},
			},
			wantIDs:  []string{"tel:+15550000015", "tel:+15550000015"},
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
			name: "multiple self aliases remain distinct",
			contacts: []*imessage.Contact{{
				FirstName: "SelfAliases",
				Phones:    []string{"+15559999999"},
				Emails:    []string{"self@example.com"},
			}},
			portalIDs: []string{"mailto:self@example.com", "tel:+15559999999"},
			selfIDs: map[string]bool{
				"mailto:self@example.com": true,
				"tel:+15559999999":        true,
			},
			wantIDs:  []string{"mailto:self@example.com", "tel:+15559999999"},
			wantSkip: map[int]bool{},
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

func TestPreferredExistingDMPortalSpellingChoosesPopulatedLegacyRoom(t *testing.T) {
	tests := []struct {
		name              string
		identifier        string
		normalizedMatches []string
		existing          map[string]existingDMPortalCandidate
		want              existingDMPortalCandidate
	}{
		{
			name:              "phone spelling",
			identifier:        "tel:+15550000013",
			normalizedMatches: []string{"tel:+15550000013", "tel:15550000013", "tel:5550000013"},
			existing: map[string]existingDMPortalCandidate{
				"tel:+15550000013": {ID: "tel:+15550000013"},
				"tel:15550000013":  {ID: "tel:15550000013", HasMessages: true},
				"tel:5550000013":   {ID: "tel:5550000013"},
			},
			want: existingDMPortalCandidate{ID: "tel:15550000013", HasMessages: true},
		},
		{
			name:              "email case spelling",
			identifier:        "mailto:person@example.com",
			normalizedMatches: []string{"mailto:person@example.com", "mailto:Person@Example.com"},
			existing: map[string]existingDMPortalCandidate{
				"mailto:person@example.com": {ID: "mailto:person@example.com"},
				"mailto:Person@Example.com": {ID: "mailto:Person@Example.com", HasMessages: true},
			},
			want: existingDMPortalCandidate{ID: "mailto:Person@Example.com", HasMessages: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preferredExistingDMPortalSpelling(
				tt.identifier,
				tt.normalizedMatches,
				func(candidate string) existingDMPortalCandidate { return tt.existing[candidate] },
			)
			if got != tt.want {
				t.Fatalf("preferred legacy spelling = %#v, want populated room %#v", got, tt.want)
			}
		})
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
	exactGUIDs := []string{"iMessage;-;mixed@example.com", "SMS;-;+15550000021(smsft)"}
	meta, changed := portalMetadataWithSMSRouting(
		&PortalMetadata{
			IsSms:          true,
			SMSDestination: "tel:+15550000021",
			ChatDBGUIDs:    exactGUIDs,
		},
		false,
		"",
	)
	if !changed {
		t.Fatal("retained iMessage representative did not change stale SMS metadata")
	}
	if meta.IsSms || meta.SMSDestination != "" {
		t.Fatalf("retained iMessage metadata = %+v, want non-SMS with no destination", meta)
	}
	if !reflect.DeepEqual(meta.ChatDBGUIDs, exactGUIDs) {
		t.Fatalf("SMS routing update changed exact GUIDs: %#v", meta.ChatDBGUIDs)
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

func TestPortalToConversationUsesPersistedSMSDestination(t *testing.T) {
	const (
		canonical   = "mailto:mixed@example.com"
		destination = "tel:+15550000022"
		self        = "tel:+15559999999"
	)
	client := &IMClient{
		handle:     self,
		allHandles: []string{self},
		smsPortals: map[string]bool{canonical: true},
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

func TestMergeChatDBInitialSyncEntriesUsesNewestServiceState(t *testing.T) {
	portalKey := networkid.PortalKey{
		ID:       networkid.PortalID("tel:+15550000001"),
		Receiver: networkid.UserLoginID("login"),
	}
	iMessageInfo := &imessage.ChatInfo{JSONChatGUID: "iMessage;-;alias@example.com"}
	smsInfo := &imessage.ChatInfo{JSONChatGUID: "SMS;-;+15550000001(smsft)"}

	tests := []struct {
		name            string
		entries         []chatDBInitialSyncEntry
		wantRep         *imessage.ChatInfo
		wantIDs         []string
		wantSMS         bool
		wantDestination string
	}{
		{
			name: "newer iMessage after SMS upgrade",
			entries: []chatDBInitialSyncEntry{
				{chatGUIDs: []string{iMessageInfo.JSONChatGUID}, portalKey: portalKey, info: iMessageInfo},
				{
					chatGUIDs:      []string{smsInfo.JSONChatGUID},
					portalKey:      portalKey,
					info:           smsInfo,
					isSms:          true,
					smsDestination: "tel:+15550000001",
				},
			},
			wantRep: iMessageInfo,
			wantIDs: []string{iMessageInfo.JSONChatGUID, smsInfo.JSONChatGUID},
		},
		{
			name: "newer SMS after iMessage fallback",
			entries: []chatDBInitialSyncEntry{
				{
					chatGUIDs:      []string{smsInfo.JSONChatGUID},
					portalKey:      portalKey,
					info:           smsInfo,
					isSms:          true,
					smsDestination: "tel:+15550000001",
				},
				{chatGUIDs: []string{iMessageInfo.JSONChatGUID}, portalKey: portalKey, info: iMessageInfo},
			},
			wantRep:         smsInfo,
			wantIDs:         []string{smsInfo.JSONChatGUID, iMessageInfo.JSONChatGUID},
			wantSMS:         true,
			wantDestination: "tel:+15550000001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged := mergeChatDBInitialSyncEntries(tt.entries)
			if len(merged) != 1 {
				t.Fatalf("merged entry count = %d, want 1", len(merged))
			}
			if merged[0].isSms != tt.wantSMS {
				t.Fatalf("merged IsSms = %v, want %v from newest exact GUID", merged[0].isSms, tt.wantSMS)
			}
			if merged[0].info != tt.wantRep {
				t.Fatalf("representative ChatInfo = %p, want first entry %p", merged[0].info, tt.wantRep)
			}
			if !reflect.DeepEqual(merged[0].chatGUIDs, tt.wantIDs) {
				t.Fatalf("merged exact GUIDs = %#v, want %#v", merged[0].chatGUIDs, tt.wantIDs)
			}
			if merged[0].smsDestination != tt.wantDestination {
				t.Fatalf("merged SMS destination = %q, want retained representative %q", merged[0].smsDestination, tt.wantDestination)
			}

			client := &IMClient{
				handle:     "tel:+15559999999",
				smsPortals: map[string]bool{string(portalKey.ID): !tt.wantSMS},
			}
			client.updatePortalSMS(string(merged[0].portalKey.ID), merged[0].isSms)
			if got := client.isPortalSMS(string(portalKey.ID)); got != tt.wantSMS {
				t.Fatalf("canonical portal live SMS routing = %v, want %v", got, tt.wantSMS)
			}
			portal := &bridgev2.Portal{Portal: &database.Portal{PortalKey: portalKey}}
			if got := client.portalToConversation(portal).IsSms; got != tt.wantSMS {
				t.Fatalf("outbound conversation IsSms = %v, want %v", got, tt.wantSMS)
			}
		})
	}
}

func TestMergeChatDBInitialSyncEntriesKeepsSMSStatePortalScoped(t *testing.T) {
	receiver := networkid.UserLoginID("login")
	iMessageKey := networkid.PortalKey{ID: networkid.PortalID("mailto:person@example.com"), Receiver: receiver}
	smsKey := networkid.PortalKey{ID: networkid.PortalID("tel:242733"), Receiver: receiver}
	entries := []chatDBInitialSyncEntry{
		{
			chatGUIDs: []string{"iMessage;-;person@example.com"},
			portalKey: iMessageKey,
			info:      &imessage.ChatInfo{JSONChatGUID: "iMessage;-;person@example.com"},
		},
		{
			chatGUIDs: []string{"SMS;-;242733"},
			portalKey: smsKey,
			info:      &imessage.ChatInfo{JSONChatGUID: "SMS;-;242733"},
			isSms:     true,
		},
	}

	merged := mergeChatDBInitialSyncEntries(entries)
	if len(merged) != 2 {
		t.Fatalf("merged entry count = %d, want 2", len(merged))
	}
	if merged[0].isSms {
		t.Fatal("unrelated iMessage portal inherited SMS routing state")
	}
	if !merged[1].isSms {
		t.Fatal("SMS portal lost its routing state")
	}
}

func TestUpdateInitialSyncPortalMetadataUnionsRetriesAndIsIdempotent(t *testing.T) {
	oldGUID := "SMS;-;+15550000001(sms)"
	newGUID := "SMS;-;+15550000001(smsft)"
	originalMetadata := &PortalMetadata{
		ThreadID:       "keep",
		IsSms:          true,
		SMSDestination: "tel:+15550000001",
		ChatDBGUIDs:    []string{oldGUID},
	}
	portal := &bridgev2.Portal{Portal: &database.Portal{Metadata: originalMetadata}}

	saveCalls := 0
	changed, err := updateInitialSyncPortalMetadata(
		context.Background(),
		portal,
		false,
		"",
		[]string{newGUID, oldGUID},
		func(context.Context, *bridgev2.Portal) error {
			saveCalls++
			return errors.New("temporary database failure")
		},
	)
	if err == nil || changed {
		t.Fatalf("failed metadata update = changed %v err %v, want false and error", changed, err)
	}
	if saveCalls != 1 {
		t.Fatalf("failed metadata update save calls = %d, want 1", saveCalls)
	}
	if portal.Metadata != originalMetadata {
		t.Fatal("failed metadata update did not restore the original metadata object")
	}
	if originalMetadata.ThreadID != "keep" || !originalMetadata.IsSms ||
		originalMetadata.SMSDestination != "tel:+15550000001" ||
		!reflect.DeepEqual(originalMetadata.ChatDBGUIDs, []string{oldGUID}) {
		t.Fatalf("failed metadata update mutated original metadata: %#v", originalMetadata)
	}

	changed, err = updateInitialSyncPortalMetadata(
		context.Background(),
		portal,
		false,
		"",
		[]string{newGUID, oldGUID},
		func(context.Context, *bridgev2.Portal) error {
			saveCalls++
			return nil
		},
	)
	if err != nil || !changed {
		t.Fatalf("retry metadata update = changed %v err %v, want true and nil", changed, err)
	}
	meta := portal.Metadata.(*PortalMetadata)
	if meta.ThreadID != "keep" || meta.IsSms || meta.SMSDestination != "" {
		t.Fatalf("successful metadata retry lost unrelated fields or retained stale SMS routing: %#v", meta)
	}
	if want := []string{oldGUID, newGUID}; !reflect.DeepEqual(meta.ChatDBGUIDs, want) {
		t.Fatalf("successful metadata retry GUIDs = %#v, want %#v", meta.ChatDBGUIDs, want)
	}

	changed, err = updateInitialSyncPortalMetadata(
		context.Background(),
		portal,
		false,
		"",
		[]string{newGUID, oldGUID},
		func(context.Context, *bridgev2.Portal) error {
			saveCalls++
			return nil
		},
	)
	if err != nil || changed {
		t.Fatalf("idempotent metadata update = changed %v err %v, want false and nil", changed, err)
	}
	if saveCalls != 2 {
		t.Fatalf("metadata update save calls = %d, want 2 with idempotent retry unsaved", saveCalls)
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
