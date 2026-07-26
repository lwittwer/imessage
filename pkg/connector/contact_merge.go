// corten-matrix - A Matrix-iMessage puppeting bridge.
// Copyright (C) 2024 Ludvig Rhodin
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package connector

// Contact-based DM portal merging.
//
// When a contact has multiple phone numbers or emails, iMessage stores each
// as a separate conversation. Without merging, the bridge creates separate
// Matrix rooms for each number. This file provides helpers to redirect
// incoming messages from a secondary phone number to an existing primary portal.

import (
	"context"
	"sort"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/lrhodin/corten-matrix/imessage"
)

// chooseContactPortalID applies the shared DM identity policy: preserve the
// best existing exact portal key, otherwise use the deterministic preferred
// contact handle for a fresh multi-handle contact.
func chooseContactPortalID(
	identifier string,
	contact *imessage.Contact,
	isSelf func(string) bool,
	findExisting func([]string) (existingDMPortalCandidate, error),
) (networkid.PortalID, error) {
	defaultID := networkid.PortalID(identifier)
	candidates, preferFreshContactID := liveContactPortalIDCandidates(identifier, contact, isSelf)
	if findExisting != nil {
		chosen, err := findExisting(candidates)
		if err != nil {
			return defaultID, err
		}
		if chosen.ID != "" {
			return networkid.PortalID(chosen.ID), nil
		}
	}
	if preferFreshContactID {
		return networkid.PortalID(candidates[0]), nil
	}
	return defaultID, nil
}

func chooseExistingDMPortalID(
	identifier string,
	contact *imessage.Contact,
	isSelf func(string) bool,
	findExisting func([]string) (existingDMPortalCandidate, error),
) (networkid.PortalID, error) {
	defaultID := networkid.PortalID(identifier)
	candidates, _ := liveContactPortalIDCandidates(identifier, contact, isSelf)
	if findExisting == nil {
		return defaultID, nil
	}
	chosen, err := findExisting(candidates)
	if err != nil {
		return defaultID, err
	}
	if chosen.ID != "" {
		return networkid.PortalID(chosen.ID), nil
	}
	return defaultID, nil
}

// liveContactPortalIDCandidates keeps self-chat identity out of contact alias
// merging. A contact record can contain both one of our handles and a peer
// handle, and treating that record as a normal alias set would redirect the
// peer's DM onto our self portal. Existing exact/legacy spellings of the
// incoming identifier are still checked by returning that identifier alone.
func liveContactPortalIDCandidates(
	identifier string,
	contact *imessage.Contact,
	isSelf func(string) bool,
) (candidates []string, preferFreshContactID bool) {
	candidates = []string{identifier}
	if isSelf != nil && isSelf(identifier) {
		return candidates, false
	}
	if contact == nil || !contact.HasName() {
		return candidates, false
	}
	contactCandidates := preferredContactPortalIDs(contact)
	if len(contactCandidates) <= 1 {
		return candidates, false
	}
	if contactHasSelfPortalID(contact, isSelf) {
		return candidates, false
	}
	return contactCandidates, true
}

func contactHasSelfPortalID(contact *imessage.Contact, isSelf func(string) bool) bool {
	if contact == nil || isSelf == nil {
		return false
	}
	for _, candidate := range contactPortalIDs(contact) {
		if isSelf(candidate) {
			return true
		}
	}
	return false
}

// resolveContactPortalID checks if the given DM identifier belongs to a contact
// with multiple handles. Existing populated rooms win; when no room exists,
// fresh live traffic uses the same deterministic phone-first identity as
// chat.db initial sync.
func (c *IMClient) resolveContactPortalID(identifier string) networkid.PortalID {
	defaultID := networkid.PortalID(identifier)

	if strings.Contains(identifier, ",") {
		return defaultID
	}

	var contact *imessage.Contact
	if !c.isMyHandle(identifier) {
		contact = c.lookupContact(identifier)
	}
	chosen, err := chooseContactPortalID(
		identifier,
		contact,
		c.isMyHandle,
		c.findExistingDMPortalCandidateForCandidates,
	)
	if err != nil {
		c.UserLogin.Log.Warn().Err(err).
			Str("original", logSafeHandle(identifier)).
			Msg("Failed to check existing contact portals; preserving original portal ID")
		return defaultID
	}
	if chosen != defaultID {
		c.UserLogin.Log.Debug().
			Str("original", logSafeHandle(identifier)).
			Str("resolved", logSafeHandle(string(chosen))).
			Msg("Resolved contact portal to stable contact identity")
	}
	return chosen
}

// validateTargetsSafe wraps Client.ValidateTargets with a recover guard.
// The call crosses into the identity-manager FFI path, which has reachable
// panic sites upstream (identity_manager.rs:249/335/542/555); a panic must
// not crash the bridge, so it degrades to "nothing validated" (nil). Shared
// by the send path and user-triggered commands.
func (c *IMClient) validateTargetsSafe(targets []string) (valid []string) {
	if c.client == nil || len(targets) == 0 {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			c.UserLogin.Log.Error().Interface("panic", r).Int("targets", len(targets)).
				Msg("ValidateTargets panicked in FFI path")
			valid = nil
		}
	}()
	return c.client.ValidateTargets(targets, c.handle)
}

// resolveSendTarget determines the best identifier to send to for a DM portal.
func (c *IMClient) resolveSendTarget(portalID string) string {
	if c.client == nil || strings.Contains(portalID, ",") {
		return portalID
	}

	contact := c.lookupContact(portalID)
	altIDs := contactPortalIDs(contact)
	if contact == nil || len(altIDs) <= 1 {
		return portalID
	}

	// Validate the portal's own handle alone first. In the common case it is
	// reachable and this stays a single-handle IDS query; including the
	// alternates here would fetch keys for every handle of the contact on the
	// send path (unregistered handles are only cached for EMPTY_REFRESH = 1h
	// rust-side, so dead alternates would be re-fetched from Apple hourly),
	// and it would couple the portal handle's validation to the alternates'
	// failure domain — one erroring batch would blank out everything.
	if valid := c.validateTargetsSafe([]string{portalID}); len(valid) > 0 {
		return portalID
	}

	alternates := make([]string, 0, len(altIDs))
	for _, altID := range altIDs {
		if altID != portalID {
			alternates = append(alternates, altID)
		}
	}

	c.UserLogin.Log.Info().
		Str("portal_id", portalID).
		Int("alternates", len(alternates)).
		Msg("Portal ID not reachable on iMessage, trying alternate contact numbers")

	valid := c.validateTargetsSafe(alternates)
	validSet := make(map[string]struct{}, len(valid))
	for _, id := range valid {
		validSet[id] = struct{}{}
	}
	if picked, ok := pickSendTarget(portalID, alternates, validSet); ok {
		c.UserLogin.Log.Info().
			Str("portal_id", portalID).
			Str("send_target", picked).
			Int("alternates", len(alternates)).
			Int("valid", len(valid)).
			Msg("Resolved send target to alternate contact number")
		return picked
	}

	c.UserLogin.Log.Warn().
		Str("portal_id", portalID).
		Int("alternates", len(alternates)).
		Msg("No reachable number found for contact; falling back to original portal ID")
	return portalID
}

func pickSendTarget(portalID string, altIDs []string, validSet map[string]struct{}) (string, bool) {
	if _, ok := validSet[portalID]; ok {
		return portalID, true
	}
	for _, altID := range altIDs {
		if altID == portalID {
			continue
		}
		if _, ok := validSet[altID]; ok {
			return altID, true
		}
	}
	return portalID, false
}

// lookupContact resolves a portal/identifier string to a Contact using
// cloud contacts (iCloud CardDAV), falling back to chat.db contacts.
func (c *IMClient) lookupContact(identifier string) *imessage.Contact {
	localID := stripIdentifierPrefix(identifier)
	if localID == "" {
		return nil
	}

	if c.contacts != nil {
		contact, _ := c.contacts.GetContactInfo(localID)
		if contact != nil {
			return contact
		}
	}
	if c.chatDB != nil {
		contact, _ := c.chatDB.api.GetContactInfo(localID)
		return contact
	}
	return nil
}

// countNonSelfMembers counts the unique non-self members of a conversation
// across the participant list and the sender, collapsing a contact's alternate
// handles so a multi-number contact isn't double-counted. The group/DM signal
// for inbound routing: self is implicit, so >=2 other members means a group.
// Sender is included because relayed carrier groups omit self from participants.
func (c *IMClient) countNonSelfMembers(participants []string, sender *string) int {
	seen := make(map[string]bool)
	count := 0
	add := func(raw string) {
		n := normalizeIdentifierForPortalID(raw)
		if n == "" || c.isMyHandle(n) || seen[n] {
			return
		}
		seen[n] = true
		count++
		if contact := c.lookupContact(n); contact != nil {
			for _, altID := range contactPortalIDs(contact) {
				seen[altID] = true
			}
		}
	}
	for _, p := range participants {
		add(p)
	}
	if sender != nil {
		add(*sender)
	}
	return count
}

// getContactChatGUIDs returns all possible chat.db GUIDs for a DM portal,
// including GUIDs for alternate phone numbers/emails belonging to the same contact.
func (c *IMClient) getContactChatGUIDs(portalID string) []string {
	guids := portalIDToChatGUIDs(portalID)

	contact := c.lookupContact(portalID)
	if contact == nil {
		return guids
	}

	for _, altID := range contactPortalIDs(contact) {
		if altID == portalID {
			continue
		}
		guids = append(guids, portalIDToChatGUIDs(altID)...)
	}

	return guids
}

// contactKeyFromContact returns a stable identity key for grouping a contact's
// DM entries during initial sync deduplication. Returns "" if no merging is
// needed (single handle, no name, etc.).
func contactKeyFromContact(contact *imessage.Contact) string {
	if contact == nil || !contact.HasName() {
		return ""
	}
	portalIDs := contactPortalIDs(contact)
	if len(portalIDs) <= 1 {
		return ""
	}
	sort.Strings(portalIDs)
	return strings.Join(portalIDs, "|")
}

// contactPortalIDs returns all portal ID strings for a contact's phone numbers
// and emails.
func contactPortalIDs(contact *imessage.Contact) []string {
	if contact == nil {
		return nil
	}

	seen := make(map[string]bool)
	var ids []string

	for _, phone := range contact.Phones {
		normalized := normalizePhoneForPortalID(phone)
		if normalized == "" {
			continue
		}
		pid := "tel:" + normalized
		if !seen[pid] {
			seen[pid] = true
			ids = append(ids, pid)
		}
	}

	for _, email := range contact.Emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			continue
		}
		pid := "mailto:" + email
		if !seen[pid] {
			seen[pid] = true
			ids = append(ids, pid)
		}
	}

	return ids
}

// preferredContactPortalIDs returns all of a contact's handles in stable
// canonical order: phone numbers first, then emails, with each kind sorted.
func preferredContactPortalIDs(contact *imessage.Contact) []string {
	ids := contactPortalIDs(contact)
	sort.Slice(ids, func(i, j int) bool {
		iPhone := strings.HasPrefix(ids[i], "tel:")
		jPhone := strings.HasPrefix(ids[j], "tel:")
		if iPhone != jPhone {
			return iPhone
		}
		return ids[i] < ids[j]
	})
	return ids
}

// existingDMPortalIDVariants returns exact and normalized portal key spellings
// that may already exist in the bridge DB. Exact variants are kept first so an
// upgrade reuses the existing key rather than creating a normalized replacement.
func existingDMPortalIDVariants(identifier string) []string {
	seen := make(map[string]bool)
	variants := make([]string, 0, 5)
	add := func(candidate string) {
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			variants = append(variants, candidate)
		}
	}

	add(identifier)
	normalized := normalizeIdentifierForPortalID(identifier)
	add(normalized)
	if !strings.HasPrefix(normalized, "tel:") {
		return variants
	}

	local := strings.TrimPrefix(normalized, "tel:")
	if strings.HasPrefix(local, "+") {
		withoutPlus := strings.TrimPrefix(local, "+")
		add("tel:" + withoutPlus)
		if strings.HasPrefix(local, "+1") && len(local) == 12 {
			add("tel:" + strings.TrimPrefix(local, "+1"))
		}
	} else if isNumeric(local) {
		if len(local) == 10 {
			add("tel:1" + local)
		}
		if len(local) == 11 && strings.HasPrefix(local, "1") {
			add("tel:" + local[1:])
		}
	}
	return variants
}

type existingDMPortalCandidate struct {
	ID          string
	HasMessages bool
}

// findExistingDMPortalCandidateForCandidates returns the best exact existing
// portal key across all contact aliases. Populated rooms beat empty aliases;
// deterministic contact and legacy-spelling order breaks ties.
func (c *IMClient) findExistingDMPortalCandidateForCandidates(identifiers []string) (existingDMPortalCandidate, error) {
	ctx := context.Background()
	inspectDirect := func(portalID string) (existingDMPortalCandidate, error) {
		portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{
			ID:       networkid.PortalID(portalID),
			Receiver: c.UserLogin.ID,
		})
		if err != nil || portal == nil || portal.MXID == "" {
			return existingDMPortalCandidate{}, err
		}
		firstMessage, err := c.Main.Bridge.DB.Message.GetFirstPortalMessage(ctx, portal.PortalKey)
		if err != nil {
			return existingDMPortalCandidate{}, err
		}
		return existingDMPortalCandidate{ID: string(portal.ID), HasMessages: firstMessage != nil}, nil
	}

	enumerateAll := func() (existingDMPortalCandidate, error) {
		portals, err := c.Main.Bridge.GetAllPortalsWithMXID(ctx)
		if err != nil {
			return existingDMPortalCandidate{}, err
		}

		portalKeys := make(map[string]networkid.PortalKey)
		normalizedMatches := make(map[string][]string)
		for _, portal := range portals {
			if portal.Receiver != "" && portal.Receiver != c.UserLogin.ID {
				continue
			}
			portalID := string(portal.ID)
			portalKeys[portalID] = portal.PortalKey
			normalized := normalizeIdentifierForPortalID(portalID)
			normalizedMatches[normalized] = append(normalizedMatches[normalized], portalID)
		}
		for normalized := range normalizedMatches {
			sort.Strings(normalizedMatches[normalized])
		}

		messageState := make(map[string]existingDMPortalCandidate)
		var lookupErr error
		findExistingRoom := func(candidate string) existingDMPortalCandidate {
			if lookupErr != nil {
				return existingDMPortalCandidate{}
			}
			if existing, ok := messageState[candidate]; ok {
				return existing
			}
			key, ok := portalKeys[candidate]
			if !ok {
				return existingDMPortalCandidate{}
			}
			firstMessage, queryErr := c.Main.Bridge.DB.Message.GetFirstPortalMessage(ctx, key)
			if queryErr != nil {
				lookupErr = queryErr
				return existingDMPortalCandidate{}
			}
			existing := existingDMPortalCandidate{ID: candidate, HasMessages: firstMessage != nil}
			messageState[candidate] = existing
			return existing
		}
		chosen := preferredExistingDMPortalCandidate(identifiers, func(identifier string) existingDMPortalCandidate {
			normalized := normalizeIdentifierForPortalID(identifier)
			return preferredExistingDMPortalSpelling(identifier, normalizedMatches[normalized], findExistingRoom)
		})
		if lookupErr != nil {
			return existingDMPortalCandidate{}, lookupErr
		}
		return chosen, nil
	}

	return findExistingDMPortalCandidateWithFastPath(identifiers, inspectDirect, enumerateAll)
}

func findExistingDMPortalCandidateWithFastPath(
	identifiers []string,
	inspectDirect func(string) (existingDMPortalCandidate, error),
	enumerateAll func() (existingDMPortalCandidate, error),
) (existingDMPortalCandidate, error) {
	if len(identifiers) > 0 && inspectDirect != nil {
		for _, candidate := range existingDMPortalIDVariants(identifiers[0]) {
			existing, err := inspectDirect(candidate)
			if err != nil {
				return existingDMPortalCandidate{}, err
			}
			if existing.ID != "" && existing.HasMessages {
				return existing, nil
			}
		}
	}
	if enumerateAll == nil {
		return existingDMPortalCandidate{}, nil
	}
	return enumerateAll()
}

func preferredExistingDMPortalCandidate(
	candidates []string,
	findExistingRoom func(string) existingDMPortalCandidate,
) existingDMPortalCandidate {
	var chosen existingDMPortalCandidate
	if findExistingRoom == nil {
		return chosen
	}
	for _, candidate := range candidates {
		existing := findExistingRoom(candidate)
		if existing.ID == "" {
			continue
		}
		if chosen.ID == "" || (!chosen.HasMessages && existing.HasMessages) {
			chosen = existing
		}
	}
	return chosen
}

func preferredExistingDMPortalSpelling(
	identifier string,
	normalizedMatches []string,
	findExistingRoom func(string) existingDMPortalCandidate,
) existingDMPortalCandidate {
	candidates := existingDMPortalIDVariants(identifier)
	seen := make(map[string]struct{}, len(candidates)+len(normalizedMatches))
	for _, candidate := range candidates {
		seen[candidate] = struct{}{}
	}
	normalizedMatches = append([]string(nil), normalizedMatches...)
	sort.Strings(normalizedMatches)
	for _, candidate := range normalizedMatches {
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return preferredExistingDMPortalCandidate(candidates, findExistingRoom)
}

// canonicalizeChatDBInitialSyncDMPortalIDs maps each multi-handle contact to
// one portal ID and marks duplicate chat.db entries to skip. Existing Matrix
// populated rooms win so upgrades keep the populated conversation instead of
// an empty alias. Existing ties and fresh contacts use deterministic
// phone-preferred order.
func canonicalizeChatDBInitialSyncDMPortalIDs(
	portalIDs []string,
	lookupContact func(string) *imessage.Contact,
	isSelf func(string) bool,
	findExistingRoom func(string) existingDMPortalCandidate,
) (canonical []string, skip map[int]bool) {
	canonical = append([]string(nil), portalIDs...)
	skip = make(map[int]bool)

	type contactGroup struct {
		contact *imessage.Contact
		indices []int
	}
	groups := make(map[string]*contactGroup)
	for i, portalID := range portalIDs {
		if strings.Contains(portalID, ",") || strings.HasPrefix(portalID, "gid:") {
			continue
		}
		contact := lookupContact(portalID)
		key := contactKeyFromContact(contact)
		if key == "" {
			continue
		}
		if group, ok := groups[key]; ok {
			group.indices = append(group.indices, i)
		} else {
			groups[key] = &contactGroup{contact: contact, indices: []int{i}}
		}
	}

	for _, group := range groups {
		candidates := preferredContactPortalIDs(group.contact)
		if len(candidates) == 0 {
			continue
		}
		if isSelf != nil {
			selfContact := false
			for _, candidate := range candidates {
				if isSelf(candidate) {
					selfContact = true
					break
				}
			}
			if selfContact {
				continue
			}
		}
		chosen := candidates[0]
		if findExistingRoom != nil {
			chosenExisting := preferredExistingDMPortalCandidate(candidates, findExistingRoom)
			if chosenExisting.ID != "" {
				chosen = chosenExisting.ID
			}
		}

		for position, idx := range group.indices {
			canonical[idx] = chosen
			if position > 0 {
				skip[idx] = true
			}
		}
	}
	return canonical, skip
}

// canonicalizeChatDBInitialSyncDMPortalIDsWithExistingRooms loads and inspects
// the existing Matrix-room index before choosing canonical IDs. Both operations
// are required: treating an error as "no room" can permanently create a second
// alias room during the one-shot initial sync.
func canonicalizeChatDBInitialSyncDMPortalIDsWithExistingRooms(
	ctx context.Context,
	portalIDs []string,
	receiver networkid.UserLoginID,
	lookupContact func(string) *imessage.Contact,
	isSelf func(string) bool,
	loadExistingRooms func(context.Context) ([]*bridgev2.Portal, error),
	inspectExistingRoom func(*bridgev2.Portal) (existingDMPortalCandidate, error),
) (canonical []string, skip map[int]bool, err error) {
	existingPortals, err := loadExistingRooms(ctx)
	if err != nil {
		return nil, nil, err
	}

	existingByID := make(map[string]*bridgev2.Portal, len(existingPortals))
	existingByNormalizedID := make(map[string][]string)
	for _, portal := range existingPortals {
		if portal.Receiver != "" && portal.Receiver != receiver {
			continue
		}
		portalID := string(portal.ID)
		existingByID[portalID] = portal
		normalized := normalizeIdentifierForPortalID(portalID)
		existingByNormalizedID[normalized] = append(existingByNormalizedID[normalized], portalID)
	}
	for normalized := range existingByNormalizedID {
		sort.Strings(existingByNormalizedID[normalized])
	}

	inspectedByID := make(map[string]existingDMPortalCandidate)
	var inspectionErr error
	findExistingRoomByID := func(portalID string) existingDMPortalCandidate {
		if inspectionErr != nil {
			return existingDMPortalCandidate{}
		}
		portal, ok := existingByID[portalID]
		if !ok {
			return existingDMPortalCandidate{}
		}
		if inspected, ok := inspectedByID[portalID]; ok {
			return inspected
		}
		candidate := existingDMPortalCandidate{ID: portalID}
		if inspectExistingRoom != nil {
			inspected, inspectErr := inspectExistingRoom(portal)
			if inspectErr != nil {
				inspectionErr = inspectErr
				return existingDMPortalCandidate{}
			}
			candidate.HasMessages = inspected.HasMessages
		}
		inspectedByID[portalID] = candidate
		return candidate
	}
	findExistingRoom := func(portalID string) existingDMPortalCandidate {
		normalized := normalizeIdentifierForPortalID(portalID)
		return preferredExistingDMPortalSpelling(
			portalID,
			existingByNormalizedID[normalized],
			findExistingRoomByID,
		)
	}
	canonical, skip = canonicalizeChatDBInitialSyncDMPortalIDs(
		portalIDs, lookupContact, isSelf, findExistingRoom,
	)
	if inspectionErr != nil {
		return nil, nil, inspectionErr
	}
	return canonical, skip, nil
}

// normalizePhoneForPortalID converts a phone number to E.164-like format.
func normalizePhoneForPortalID(phone string) string {
	n := normalizePhone(phone)
	if n == "" {
		return ""
	}
	if strings.HasPrefix(n, "+") {
		return n
	}
	if len(n) == 10 {
		return "+1" + n
	}
	if len(n) == 11 && n[0] == '1' {
		return "+" + n
	}
	return "+" + n
}

// canonicalContactHandle returns a deterministic canonical handle for a contact
// that has multiple iMessage handles (phone + email). This ensures CloudKit
// backfill creates a single portal per contact rather than one per handle.
// If the identifier doesn't resolve to a multi-handle contact, returns it unchanged.
func (c *IMClient) canonicalContactHandle(identifier string) string {
	contact := c.lookupContact(identifier)
	if contact == nil || !contact.HasName() {
		return identifier
	}
	altIDs := preferredContactPortalIDs(contact)
	if len(altIDs) <= 1 {
		return identifier
	}
	return altIDs[0]
}

// canonicalizeDMSender remaps the sender identity for DM events so that the
// ghost matches the portal's canonical handle. Without this, a contact sending
// from their email handle into a phone-based DM portal causes a phantom ghost
// to briefly join the room.
func (c *IMClient) canonicalizeDMSender(portalKey networkid.PortalKey, sender bridgev2.EventSender) bridgev2.EventSender {
	if sender.IsFromMe {
		return sender
	}
	portalID := string(portalKey.ID)
	// Only remap for DM portals (not groups or gid: portals).
	if strings.Contains(portalID, ",") || strings.HasPrefix(portalID, "gid:") {
		return sender
	}
	canonicalUserID := makeUserID(portalID)
	if sender.Sender != canonicalUserID {
		return bridgev2.EventSender{
			IsFromMe: false,
			Sender:   canonicalUserID,
		}
	}
	return sender
}
