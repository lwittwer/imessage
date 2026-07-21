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
		existingRoom map[string]bool
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
			existingRoom: map[string]bool{"mailto:existing@example.com": true},
			wantIDs:      []string{"mailto:existing@example.com", "mailto:existing@example.com"},
			wantSkip:     map[int]bool{1: true},
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
			hasExistingRoom := func(portalID string) bool {
				return tt.existingRoom[portalID]
			}
			gotIDs, gotSkip := canonicalizeChatDBInitialSyncDMPortalIDs(
				tt.portalIDs,
				contactLookupForTests(tt.contacts...),
				hasExistingRoom,
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
