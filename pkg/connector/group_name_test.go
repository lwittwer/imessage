package connector

import (
	"testing"

	"github.com/lrhodin/imessage/imessage"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type testContactSource struct{}

func (testContactSource) SyncContacts(zerolog.Logger) error { return nil }
func (testContactSource) GetContactInfo(string) (*imessage.Contact, error) {
	return nil, nil
}
func (testContactSource) GetAllContacts() []*imessage.Contact { return nil }

func TestIsPlaceholderGroupName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"", true},
		{"   ", true},
		{"Group Chat", true},
		{"group chat", true},
		{" gRoUp ChAt ", true},
		{"The Fam", false},
		{"Group Chat 2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPlaceholderGroupName(tt.name); got != tt.want {
				t.Fatalf("isPlaceholderGroupName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestIsRawIdentifierGroupName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"alice@example.com, bob@example.com", true},
		{"+1 (415) 555-0100, 415-555-0101", true},
		{"Alice, Bob", false},
		{"Alice, bob@example.com", false},
		{"Group Chat", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRawIdentifierGroupName(tt.name); got != tt.want {
				t.Fatalf("isRawIdentifierGroupName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestShouldApplyGroupNameRefresh(t *testing.T) {
	tests := []struct {
		name          string
		currentName   string
		newName       string
		authoritative bool
		want          bool
	}{
		{"empty new rejected", "", "", true, false},
		{"whitespace new rejected", "", "  ", true, false},
		{"exact noop rejected", "The Fam", "The Fam", true, false},
		{"authoritative custom over custom allowed", "Old", "New", true, true},
		{"authoritative group chat over custom rejected", "Old", "Group Chat", true, false},
		{"fallback placeholder over empty rejected", "", "Group Chat", false, false},
		{"fallback over custom rejected", "The Fam", "Alice, Bob", false, false},
		{"fallback over empty placeholder allowed", "", "Alice, Bob", false, true},
		{"fallback over group chat placeholder allowed", "Group Chat", "Alice, Bob", false, true},
		{"fallback over mixed-case placeholder allowed", "group chat", "Alice, Bob", false, true},
		{"fallback over raw identifiers allowed", "alice@example.com, bob@example.com", "Alice, Bob", false, true},
		{"raw fallback over group chat allowed", "Group Chat", "alice@example.com, bob@example.com", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldApplyGroupNameRefresh(tt.currentName, tt.newName, tt.authoritative)
			if got != tt.want {
				t.Fatalf("shouldApplyGroupNameRefresh(%q, %q, %v) = %v, want %v", tt.currentName, tt.newName, tt.authoritative, got, tt.want)
			}
		})
	}
}

func TestMakePortalKeyPlaceholderGroupNameUsesDMPeer(t *testing.T) {
	c := &IMClient{
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: networkid.UserLoginID("test-login")}},
		allHandles: []string{
			"mailto:self@example.com",
		},
	}
	groupName := "Group Chat"
	sender := "mailto:peer@example.com"
	senderGuid := "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"

	got := c.makePortalKey(
		[]string{"mailto:self@example.com", "mailto:peer@example.com"},
		&groupName,
		&sender,
		&senderGuid,
	)

	if got.ID != "mailto:peer@example.com" {
		t.Fatalf("makePortalKey() ID = %q, want DM peer portal", got.ID)
	}
	if got.Receiver != "test-login" {
		t.Fatalf("makePortalKey() Receiver = %q, want test-login", got.Receiver)
	}
}

func TestMakePortalKeyRealTwoPersonGroupNameStaysGroup(t *testing.T) {
	c := &IMClient{
		allHandles: []string{"mailto:self@example.com"},
	}
	groupName := "Project Thread"

	if !c.isGroupPortalSignal(
		[]string{"mailto:self@example.com", "mailto:peer@example.com"},
		&groupName,
	) {
		t.Fatal("isGroupPortalSignal() rejected named two-person group")
	}
}

func TestNormalizeAndDedupeSenders(t *testing.T) {
	tests := []struct {
		name    string
		senders []string
		want    []string
	}{
		{"empty", nil, nil},
		{"drops blanks", []string{"", "  "}, nil},
		{
			name:    "normalizes phone and email",
			senders: []string{"4155550100", "User@Example.COM"},
			want:    []string{"tel:+14155550100", "mailto:user@example.com"},
		},
		{
			name:    "dedupes after normalization",
			senders: []string{"User@Example.COM", "mailto:user@example.com", "+14155550100", "4155550100"},
			want:    []string{"mailto:user@example.com", "tel:+14155550100"},
		},
		{
			name:    "preserves first seen order",
			senders: []string{"second@example.com", "first@example.com", "second@example.com"},
			want:    []string{"mailto:second@example.com", "mailto:first@example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeAndDedupeSenders(tt.senders)
			if len(got) != len(tt.want) {
				t.Fatalf("normalizeAndDedupeSenders() len = %d, want %d: %#v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("normalizeAndDedupeSenders()[%d] = %q, want %q (full result %#v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

func TestResolveGroupMembersForDisplayFallsBackBeforeContactsReady(t *testing.T) {
	c := &IMClient{
		contactsReady: false,
	}
	got := c.resolveGroupMembersForDisplay(t.Context(), "mailto:a@example.com,mailto:b@example.com")
	want := []string{"mailto:a@example.com", "mailto:b@example.com"}
	if len(got) != len(want) {
		t.Fatalf("resolveGroupMembersForDisplay() len = %d, want %d before contacts are ready: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolveGroupMembersForDisplay()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	c.contacts = testContactSource{}
	c.contactsReady = true
	got = c.resolveGroupMembersForDisplay(t.Context(), "mailto:a@example.com,mailto:b@example.com")
	if len(got) != len(want) {
		t.Fatalf("resolveGroupMembersForDisplay() len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolveGroupMembersForDisplay()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveAuthoritativeGroupNameIgnoresPlaceholder(t *testing.T) {
	c := &IMClient{
		imGroupNames: map[string]string{
			"gid:placeholder": "Group Chat",
			"gid:custom":      "The Fam",
		},
	}

	if got, ok := c.resolveAuthoritativeGroupName(t.Context(), "gid:placeholder"); ok || got != "" {
		t.Fatalf("resolveAuthoritativeGroupName() = %q, %v; want no authoritative placeholder", got, ok)
	}
	if got, ok := c.resolveAuthoritativeGroupName(t.Context(), "gid:custom"); !ok || got != "The Fam" {
		t.Fatalf("resolveAuthoritativeGroupName() = %q, %v; want custom authoritative name", got, ok)
	}
}
