package connector

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/lrhodin/corten-matrix/imessage"
)

// chatDBGUIDRefreshEntry is the non-destructive subset of chat.db chat state
// needed to attach exact Apple GUIDs to an already-existing portal.
type chatDBGUIDRefreshEntry struct {
	PortalID string
	ChatGUID string
}

// refreshChatDBGUIDMetadata enumerates chat.db and refreshes exact GUID
// metadata on existing Matrix rooms. It never creates, re-IDs, or backfills a
// portal. There is intentionally no persisted completion bit: chat.db can gain
// another exact GUID variant later, and any failed pass must retry on the next
// connect instead of being mistaken for a completed migration.
func (c *IMClient) refreshChatDBGUIDMetadata(ctx context.Context) (updated, unchanged int, err error) {
	entries, err := c.enumerateChatDBGUIDRefreshEntries()
	if err != nil {
		return 0, 0, err
	}

	portals, err := c.Main.Bridge.GetAllPortalsWithMXID(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("load existing portals: %w", err)
	}

	portalByID := make(map[string]*bridgev2.Portal)
	normalizedIDs := make(map[string][]string)
	for _, portal := range portals {
		if portal.Receiver != "" && portal.Receiver != c.UserLogin.ID {
			continue
		}
		portalID := string(portal.ID)
		portalByID[portalID] = portal
		if !strings.Contains(portalID, ",") {
			normalized := normalizeIdentifierForPortalID(portalID)
			normalizedIDs[normalized] = append(normalizedIDs[normalized], portalID)
		}
	}

	messageState := make(map[string]bool)
	messageStateKnown := make(map[string]bool)
	var resolveErr error
	toCandidate := func(portalID string) existingDMPortalCandidate {
		candidate := existingDMPortalCandidate{ID: portalID}
		if messageStateKnown[portalID] {
			candidate.HasMessages = messageState[portalID]
			return candidate
		}
		messageStateKnown[portalID] = true
		firstMessage, queryErr := c.Main.Bridge.DB.Message.GetFirstPortalMessage(ctx, portalByID[portalID].PortalKey)
		if queryErr != nil {
			resolveErr = fmt.Errorf("check existing portal messages: %w", queryErr)
			return existingDMPortalCandidate{}
		}
		messageState[portalID] = firstMessage != nil
		candidate.HasMessages = messageState[portalID]
		return candidate
	}
	findExisting := func(portalID string) existingDMPortalCandidate {
		if resolveErr != nil {
			return existingDMPortalCandidate{}
		}
		return preferredExistingDMPortalSpelling(portalID, normalizedIDs[normalizeIdentifierForPortalID(portalID)], func(candidate string) existingDMPortalCandidate {
			if _, ok := portalByID[candidate]; !ok {
				return existingDMPortalCandidate{}
			}
			return toCandidate(candidate)
		})
	}

	assignments := matchChatDBGUIDsToExistingPortals(entries, c.lookupContact, c.isMyHandle, findExisting)
	if resolveErr != nil {
		return 0, 0, resolveErr
	}
	return applyChatDBGUIDMetadataRefresh(ctx, assignments, portalByID, func(ctx context.Context, portal *bridgev2.Portal) error {
		return portal.Save(ctx)
	})
}

func (c *IMClient) enumerateChatDBGUIDRefreshEntries() ([]chatDBGUIDRefreshEntry, error) {
	chats, err := c.chatDB.api.GetChatsWithMessagesAfter(time.Time{})
	if err != nil {
		return nil, fmt.Errorf("enumerate chat.db chats: %w", err)
	}

	entries := make([]chatDBGUIDRefreshEntry, 0, len(chats))
	for _, chat := range chats {
		parsed := imessage.ParseIdentifier(chat.ChatGUID)
		if parsed.LocalID == "" {
			return nil, fmt.Errorf("chat.db returned a chat with an empty GUID")
		}
		portalID := string(identifierToPortalID(parsed))
		if parsed.IsGroup {
			info, infoErr := c.chatDB.api.GetChatInfo(chat.ChatGUID, chat.ThreadID)
			if infoErr != nil {
				return nil, fmt.Errorf("read chat.db group info: %w", infoErr)
			}
			if info == nil {
				return nil, fmt.Errorf("chat.db returned empty group info")
			}
			members := make([]string, 0, len(info.Members)+1)
			members = append(members, addIdentifierPrefix(c.handle))
			for _, member := range info.Members {
				members = append(members, addIdentifierPrefix(stripSmsSuffix(member)))
			}
			sort.Strings(members)
			portalID = strings.Join(members, ",")
		}
		entries = append(entries, chatDBGUIDRefreshEntry{PortalID: portalID, ChatGUID: chat.ChatGUID})
	}
	return entries, nil
}

func matchChatDBGUIDsToExistingPortals(
	entries []chatDBGUIDRefreshEntry,
	lookupContact func(string) *imessage.Contact,
	isSelf func(string) bool,
	findExisting func(string) existingDMPortalCandidate,
) map[string][]string {
	portalIDs := make([]string, len(entries))
	for i := range entries {
		portalIDs[i] = entries[i].PortalID
	}
	canonical, _ := canonicalizeChatDBInitialSyncDMPortalIDs(portalIDs, lookupContact, isSelf, findExisting)

	assignments := make(map[string][]string)
	for i, entry := range entries {
		candidate := findExisting(canonical[i])
		if candidate.ID == "" {
			continue
		}
		assignments[candidate.ID] = append(assignments[candidate.ID], entry.ChatGUID)
	}
	for portalID, guids := range assignments {
		assignments[portalID] = sortedUniqueStrings(guids)
	}
	return assignments
}

// updatePortalChatDBGUIDMetadata grows the exact-GUID set while preserving all
// unrelated portal metadata. A failed save restores the original metadata
// pointer so the in-memory state matches the database and a later pass retries.
func updatePortalChatDBGUIDMetadata(
	ctx context.Context,
	portal *bridgev2.Portal,
	additions []string,
	save func(context.Context, *bridgev2.Portal) error,
) (bool, error) {
	existing, _ := portal.Metadata.(*PortalMetadata)
	existingGUIDs := []string(nil)
	if existing != nil {
		existingGUIDs = existing.ChatDBGUIDs
	}
	updatedGUIDs := appendUniqueStrings(existingGUIDs, additions...)
	if stringSlicesEqual(existingGUIDs, updatedGUIDs) {
		return false, nil
	}

	oldMetadata := portal.Metadata
	refreshed := &PortalMetadata{}
	if existing != nil {
		*refreshed = *existing
	}
	refreshed.ChatDBGUIDs = append([]string(nil), updatedGUIDs...)
	portal.Metadata = refreshed
	if save != nil {
		if err := save(ctx, portal); err != nil {
			portal.Metadata = oldMetadata
			return false, err
		}
	}
	return true, nil
}

// updateInitialSyncPortalMetadata persists the exact GUID bundle together with
// the retained chat's SMS routing state. The bundle already supplies the
// in-flight forward fetch, so this explicit post-handler save can roll back on
// failure instead of relying on bridgev2 ChatInfo.ExtraUpdates, whose save
// errors are not returned to the connector.
func updateInitialSyncPortalMetadata(
	ctx context.Context,
	portal *bridgev2.Portal,
	isSms bool,
	smsDestination string,
	chatGUIDs []string,
	save func(context.Context, *bridgev2.Portal) error,
) (bool, error) {
	oldMetadata := portal.Metadata
	refreshed, changed := portalMetadataWithSMSRouting(oldMetadata, isSms, smsDestination)
	updatedGUIDs := appendUniqueStrings(refreshed.ChatDBGUIDs, chatGUIDs...)
	if !stringSlicesEqual(refreshed.ChatDBGUIDs, updatedGUIDs) {
		refreshed.ChatDBGUIDs = append([]string(nil), updatedGUIDs...)
		changed = true
	}
	if !changed {
		return false, nil
	}

	portal.Metadata = refreshed
	if save != nil {
		if err := save(ctx, portal); err != nil {
			portal.Metadata = oldMetadata
			return false, err
		}
	}
	return true, nil
}

func applyChatDBGUIDMetadataRefresh(
	ctx context.Context,
	assignments map[string][]string,
	portalByID map[string]*bridgev2.Portal,
	save func(context.Context, *bridgev2.Portal) error,
) (updated, unchanged int, err error) {
	portalIDs := make([]string, 0, len(assignments))
	for portalID := range assignments {
		portalIDs = append(portalIDs, portalID)
	}
	sort.Strings(portalIDs)

	var saveErrs []error
	for _, portalID := range portalIDs {
		portal := portalByID[portalID]
		if portal == nil {
			continue
		}
		changed, saveErr := updatePortalChatDBGUIDMetadata(ctx, portal, sortedUniqueStrings(assignments[portalID]), save)
		if saveErr != nil {
			saveErrs = append(saveErrs, fmt.Errorf("save exact chat.db GUID metadata: %w", saveErr))
			continue
		}
		if changed {
			updated++
		} else {
			unchanged++
		}
	}
	return updated, unchanged, errors.Join(saveErrs...)
}

func sortedUniqueStrings(values []string) []string {
	result := uniqueStrings(values)
	sort.Strings(result)
	return result
}
