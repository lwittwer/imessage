package connector

import "testing"

import "github.com/lrhodin/imessage/imessage"

func TestContactSnapshotIndexesEmailsAndPhoneSuffixes(t *testing.T) {
	snapshot := newContactSnapshot([]*imessage.Contact{
		{
			FirstName: "Alice",
			Phones:    []string{"+1 (555) 123-4567", "+1 (555) 123-4567"},
			Emails:    []string{"Alice@Example.com", "alice@example.com"},
		},
	})

	if got := snapshot.byEmail["alice@example.com"]; len(got) != 1 || got[0] != 0 {
		t.Fatalf("expected lowercase email key to map once, got %#v", got)
	}

	for _, key := range []string{"+15551234567", "15551234567", "5551234567", "1234567"} {
		got := snapshot.byPhone[key]
		if len(got) != 1 || got[0] != 0 {
			t.Fatalf("expected phone key %q to map once, got %#v", key, got)
		}
	}
}

func TestContactSnapshotLookupUniqueVsAmbiguous(t *testing.T) {
	snapshot := newContactSnapshot([]*imessage.Contact{
		{FirstName: "Alice", Phones: []string{"+1 (555) 123-4567"}, Emails: []string{"alice@example.com"}},
		{FirstName: "Bob", Phones: []string{"+1 (555) 999-4567"}, Emails: []string{"bob@example.com"}},
	})

	if contact := snapshot.lookup("alice@example.com"); contact == nil || contact.FirstName != "Alice" {
		t.Fatalf("expected unique email lookup to resolve Alice, got %#v", contact)
	}
	if contact := snapshot.lookup("+15551234567"); contact == nil || contact.FirstName != "Alice" {
		t.Fatalf("expected unique phone lookup to resolve Alice, got %#v", contact)
	}
	if contact := snapshot.lookup("4567"); contact != nil {
		t.Fatalf("expected ambiguous short suffix lookup to fail, got %#v", contact)
	}
	if contact := snapshot.lookup("missing@example.com"); contact != nil {
		t.Fatalf("expected missing lookup to fail, got %#v", contact)
	}
}
