package connector

import (
	"strings"

	"github.com/lrhodin/imessage/imessage"
)

type contactSnapshot struct {
	contacts []*imessage.Contact
	byPhone  map[string][]int
	byEmail  map[string][]int
}

func newContactSnapshot(contacts []*imessage.Contact) *contactSnapshot {
	snapshot := &contactSnapshot{
		contacts: contacts,
		byPhone:  make(map[string][]int),
		byEmail:  make(map[string][]int),
	}
	for idx, contact := range contacts {
		seenPhones := make(map[string]struct{})
		for _, phone := range contact.Phones {
			for _, suffix := range phoneSuffixes(phone) {
				if _, seen := seenPhones[suffix]; seen {
					continue
				}
				seenPhones[suffix] = struct{}{}
				snapshot.byPhone[suffix] = append(snapshot.byPhone[suffix], idx)
			}
		}

		seenEmails := make(map[string]struct{})
		for _, email := range contact.Emails {
			key := strings.ToLower(email)
			if _, seen := seenEmails[key]; seen {
				continue
			}
			seenEmails[key] = struct{}{}
			snapshot.byEmail[key] = append(snapshot.byEmail[key], idx)
		}
	}
	return snapshot
}

func (s *contactSnapshot) getAllContacts() []*imessage.Contact {
	if s == nil || len(s.contacts) == 0 {
		return nil
	}
	result := make([]*imessage.Contact, len(s.contacts))
	copy(result, s.contacts)
	return result
}

func (s *contactSnapshot) lookup(localID string) *imessage.Contact {
	if s == nil || localID == "" {
		return nil
	}

	var matches []int
	if strings.Contains(localID, "@") {
		matches = s.byEmail[strings.ToLower(localID)]
	} else {
		candidateSet := make(map[int]struct{})
		for _, suffix := range phoneSuffixes(localID) {
			for _, idx := range s.byPhone[suffix] {
				candidateSet[idx] = struct{}{}
			}
		}
		if len(candidateSet) == 0 {
			return nil
		}
		matches = make([]int, 0, len(candidateSet))
		for idx := range candidateSet {
			matches = append(matches, idx)
		}
	}

	if len(matches) != 1 {
		return nil
	}
	return s.contacts[matches[0]]
}

type localDisplayLookup interface {
	lookupDisplayContact(localID string) *imessage.Contact
}
