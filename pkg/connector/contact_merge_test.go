package connector

import (
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

func TestCanonicalizeChatDBInitialSyncDMPortalIDs(t *testing.T) {
	tests := []struct {
		name         string
		contacts     []*imessage.Contact
		portalIDs    []string
		existingRoom map[string]string
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
			portalIDs:    []string{"tel:+15550000003", "mailto:existing@example.com"},
			existingRoom: map[string]string{"mailto:existing@example.com": "mailto:existing@example.com"},
			wantIDs:      []string{"mailto:existing@example.com", "mailto:existing@example.com"},
			wantSkip:     map[int]bool{1: true},
		},
		{
			name: "existing legacy phone portal keeps exact key",
			contacts: []*imessage.Contact{{
				FirstName: "LegacyPhone",
				Phones:    []string{"+15550000013"},
				Emails:    []string{"legacy@example.com"},
			}},
			portalIDs:    []string{"mailto:legacy@example.com"},
			existingRoom: map[string]string{"tel:+15550000013": "tel:15550000013"},
			wantIDs:      []string{"tel:15550000013"},
			wantSkip:     map[int]bool{},
		},
		{
			name: "existing mixed case email portal keeps exact key",
			contacts: []*imessage.Contact{{
				FirstName: "LegacyEmail",
				Emails:    []string{"Person@Example.com", "other@example.com"},
			}},
			portalIDs:    []string{"mailto:other@example.com"},
			existingRoom: map[string]string{"mailto:person@example.com": "mailto:Person@Example.com"},
			wantIDs:      []string{"mailto:Person@Example.com"},
			wantSkip:     map[int]bool{},
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
			findExistingRoom := func(portalID string) string {
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
