package connector

import (
	"strings"

	"github.com/lrhodin/imessage/imessage"
)

func (c *IMClient) lookupContactForDisplay(identifier string) (contact *imessage.Contact, localID string, err error) {
	localID = stripIdentifierPrefix(identifier)
	if localID == "" || strings.HasPrefix(identifier, "gid:") || strings.Contains(identifier, ",") {
		return nil, localID, nil
	}
	if c.contacts == nil {
		return nil, localID, nil
	}

	contact, err = c.contacts.GetContactInfo(localID)
	if err != nil || contact != nil {
		return contact, localID, err
	}

	displayLookup, ok := c.contacts.(localDisplayLookup)
	if !ok {
		return nil, localID, nil
	}
	return displayLookup.lookupDisplayContact(localID), localID, nil
}
