// corten-matrix - A Matrix-iMessage puppeting bridge.
// Copyright (C) 2024 Ludvig Rhodin
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ffmpeg"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/lrhodin/corten-matrix/imessage"
)

// chatDB wraps the macOS chat.db iMessage API for backfill and contact
// resolution. It does NOT listen for incoming messages (rustpush handles that).
type chatDB struct {
	api imessage.API
}

type chatDBBackfillableProber interface {
	HasBackfillableMessagesBefore(chatID string, before time.Time, limit int) (bool, error)
}

type chatDBBackfillCursorPosition struct {
	TimeNS int64 `json:"time_ns"`
	RowID  int   `json:"row_id"`
}

type chatDBBackfillGUIDBundle struct {
	// ChatGUIDs are literal values selected from the fresh chat.db
	// enumeration that queued the initial-sync or restore resync.
	ChatGUIDs []string
}

// openChatDB attempts to open the local iMessage chat.db database.
// Returns nil if chat.db is not accessible (e.g., no Full Disk Access).
func openChatDB(log zerolog.Logger) *chatDB {
	if !canReadChatDB(log) {
		showDialogAndOpenFDA(log)
		waitForFDA(log)
	}

	adapter := newBridgeAdapter(&log)
	api, err := imessage.NewAPI(adapter)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to initialize chat.db API via imessage.NewAPI")
		return nil
	}

	return &chatDB{api: api}
}

// Close stops the chat.db API.
func (db *chatDB) Close() {
	if db.api != nil {
		db.api.Stop()
	}
}

// findGroupChatGUID finds a group chat GUID by matching the portal's members.
// The portalID is comma-separated members like "tel:+1555...,tel:+1555...".
func (db *chatDB) findGroupChatGUID(portalID string, c *IMClient) string {
	// Parse members from portal ID (lowercase for case-insensitive matching)
	portalMembers := strings.Split(portalID, ",")
	portalMemberSet := make(map[string]struct{})
	for _, m := range portalMembers {
		portalMemberSet[strings.ToLower(stripIdentifierPrefix(m))] = struct{}{}
	}

	// Search all group chats
	chats, err := db.api.GetChatsWithMessagesAfter(time.Time{})
	if err != nil {
		return ""
	}

	for _, chat := range chats {
		parsed := imessage.ParseIdentifier(chat.ChatGUID)
		if !parsed.IsGroup {
			continue
		}
		info, err := db.api.GetChatInfo(chat.ChatGUID, chat.ThreadID)
		if err != nil || info == nil {
			continue
		}

		// Build member set from chat.db (add self, lowercase for case-insensitive matching)
		chatMemberSet := make(map[string]struct{})
		chatMemberSet[strings.ToLower(stripIdentifierPrefix(c.handle))] = struct{}{}
		for _, m := range info.Members {
			chatMemberSet[strings.ToLower(stripIdentifierPrefix(m))] = struct{}{}
		}

		// Check if members match
		if len(chatMemberSet) == len(portalMemberSet) {
			match := true
			for m := range portalMemberSet {
				if _, ok := chatMemberSet[m]; !ok {
					match = false
					break
				}
			}
			if match {
				return chat.ChatGUID
			}
		}
	}
	return ""
}

// chatDBReplyTarget returns the correct MessageOptionalPartID for a reply,
// mapping chat.db balloon-part index to the emitted part IDs:
// bp<=0 -> base GUID (text body); bp>=1 -> {guid}_att{bp-1} (attachment).
// Negative part values are normalized to 0 (base-message semantics).
func chatDBReplyTarget(replyGUID string, replyPart int) *networkid.MessageOptionalPartID {
	targetID := replyGUID
	if replyPart >= 1 {
		targetID = fmt.Sprintf("%s_att%d", replyGUID, replyPart-1)
	}
	return &networkid.MessageOptionalPartID{MessageID: makeMessageID(targetID)}
}

// FetchMessages retrieves historical messages from chat.db for backfill.
func (db *chatDB) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams, c *IMClient) (*bridgev2.FetchMessagesResponse, error) {
	portalID := string(params.Portal.ID)
	log := zerolog.Ctx(ctx)

	// Re-enumerate exact variants at the start of each real backfill session:
	// forward backfill is one-shot, while an empty cursor identifies the first
	// backward page. A resumed backward cursor keeps its original GUID set.
	refreshExactGUIDs := params.Forward || params.Cursor == ""
	chatGUIDs, err := db.chatGUIDsForPortal(ctx, params.Portal, c, params.BundledData, refreshExactGUIDs)
	if err != nil {
		return nil, err
	}

	log.Info().Str("portal_id", portalID).Strs("chat_guids", chatGUIDs).Bool("forward", params.Forward).Msg("FetchMessages called")

	if len(chatGUIDs) == 0 {
		log.Warn().Str("portal_id", portalID).Msg("Could not find chat GUID for portal")
		return &bridgev2.FetchMessagesResponse{HasMore: false, Forward: params.Forward}, nil
	}

	count := params.Count
	if count <= 0 {
		count = 50
	}

	// Fetch messages from ALL chat GUIDs and merge. For contacts with multiple
	// phone numbers, this combines messages from all numbers into one timeline.
	// Also tries multiple GUID formats (any/iMessage/SMS) per phone number.
	var messages []*imessage.Message
	var lastErr error

	// Respect the configured message cap. params.Count comes from the
	// framework's MaxInitialMessages setting.
	maxMessages := c.Main.Bridge.Config.Backfill.MaxInitialMessages
	cursorTimes := decodeChatDBBackfillCursor(params.Cursor, chatGUIDs)
	hasBackwardCursor := !params.Forward && len(cursorTimes) > 0
	messagesByGUID := make(map[string][]*imessage.Message, len(chatGUIDs))

	for _, chatGUID := range chatGUIDs {
		var msgs []*imessage.Message
		cursorPos, useCursor, exhausted := chatDBCursorStateForGUID(cursorTimes, chatGUID, hasBackwardCursor)
		if exhausted {
			messagesByGUID[chatGUID] = nil
			continue
		}
		if useCursor {
			msgs, lastErr = db.api.GetMessagesBeforeCursor(chatGUID, time.Unix(0, cursorPos.TimeNS), cursorPos.RowID, count)
		} else if params.AnchorMessage != nil {
			if params.Forward {
				msgs, lastErr = db.api.GetMessagesSinceDate(chatGUID, params.AnchorMessage.Timestamp, "")
			} else {
				msgs, lastErr = db.api.GetMessagesBeforeWithLimit(chatGUID, params.AnchorMessage.Timestamp, count)
			}
		} else {
			// Fetch the most recent N messages (uncapped = MaxInt32, effectively all).
			msgs, lastErr = db.api.GetMessagesBeforeWithLimit(chatGUID, time.Now().Add(time.Minute), maxMessages)
		}
		if lastErr != nil {
			log.Error().Err(lastErr).Str("chat_guid", chatGUID).Strs("chat_guids", chatGUIDs).Msg("Failed to fetch messages from chat.db")
			return nil, fmt.Errorf("failed to fetch messages from chat.db for %s: %w", chatGUID, lastErr)
		}
		messages = append(messages, msgs...)
		messagesByGUID[chatGUID] = msgs
	}

	// Cursor state is intentionally calculated from the raw per-GUID pages.
	// The same chat_message row can be joined through multiple exact chat GUIDs,
	// so deduplicate the merged event stream before conversion.
	messages = deduplicateChatDBMessages(messages)

	// Sort chronologically — messages may come from multiple chat GUIDs
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Time.Before(messages[j].Time)
	})

	log.Info().Strs("chat_guids", chatGUIDs).Int("raw_message_count", len(messages)).Msg("Got messages from chat.db")
	nextCursor := encodeChatDBBackfillCursor(messagesByGUID, count, params.Forward)
	hasMore := len(messages) >= count
	if !params.Forward {
		hasMore = nextCursor != ""
	}

	// Get an intent for uploading media. The bot intent works for all uploads.
	intent := c.Main.Bridge.Bot

	backfillMessages := make([]*bridgev2.BackfillMessage, 0, len(messages))
	for _, msg := range messages {
		if !chatDBMessageCanBackfill(msg) {
			continue
		}
		sender := chatDBMakeEventSender(msg, c)
		sender = c.canonicalizeDMSender(params.Portal.PortalKey, sender)

		normalizeChatDBMessageText(msg)

		// Only create a text part if there's actual text content
		if msg.Text != "" || msg.Subject != "" {
			cm, err := convertChatDBMessage(ctx, params.Portal, intent, msg, c.Main.Config.URLPreviewsInBackfill)
			if err == nil {
				backfillMessages = append(backfillMessages, &bridgev2.BackfillMessage{
					ConvertedMessage: cm,
					Sender:           sender,
					ID:               makeMessageID(msg.GUID),
					TxnID:            networkid.TransactionID(msg.GUID),
					Timestamp:        msg.Time,
					StreamOrder:      msg.Time.UnixMilli(),
				})
			}
		}

		for i, att := range msg.Attachments {
			if att == nil {
				continue
			}
			attCm, err := convertChatDBAttachment(ctx, params.Portal, intent, msg, att, c.Main.Config.VideoTranscoding, c.Main.Config.HEICConversion, c.Main.Config.HEICJPEGQuality)
			if err != nil {
				log.Warn().Err(err).Str("guid", msg.GUID).Int("att_index", i).Msg("Failed to convert attachment, skipping")
				continue
			}
			partID := chatDBAttachmentMessagePartID(msg.GUID, i, attCm)
			if !chatDBAttachmentIsNotice(attCm) {
				noticeID := makeMessageID(fmt.Sprintf("%s_att%d_notice", msg.GUID, i))
				c.redactBackfillNotice(ctx, params.Portal, noticeID)
				c.redactTransientBackfillNotice(ctx, params.Portal, makeMessageID(partID))
				if i == 0 && msg.Text == "" && msg.Subject == "" {
					c.redactTransientBackfillNotice(ctx, params.Portal, makeMessageID(msg.GUID))
				}
			}
			if msg.ReplyToGUID != "" {
				attCm.ReplyTo = chatDBReplyTarget(msg.ReplyToGUID, msg.ReplyToPart)
			}
			backfillMessages = append(backfillMessages, &bridgev2.BackfillMessage{
				ConvertedMessage: attCm,
				Sender:           sender,
				ID:               makeMessageID(partID),
				TxnID:            networkid.TransactionID(partID),
				Timestamp:        msg.Time.Add(time.Duration(i+1) * time.Millisecond),
				StreamOrder:      msg.Time.UnixMilli() + int64(i+1),
			})
		}
	}

	return &bridgev2.FetchMessagesResponse{
		Messages: backfillMessages,
		Cursor:   nextCursor,
		HasMore:  hasMore,
		Forward:  params.Forward,
		// A shared chat_message row may fall on different per-GUID backward
		// pages. In-page deduplication cannot suppress a copy already persisted
		// by live APNs or an earlier page, so multi-GUID backward sessions need
		// the framework's database-level deduplication too.
		AggressiveDeduplication: params.Forward || len(chatGUIDs) > 1,
	}, nil
}

// chatGUIDsForPortal returns the exact chat.db GUID set saved while deciding
// whether an initial-sync portal has backfillable content. The saved set is
// authoritative: in particular, Apple SMS-forwarding GUID suffixes cannot be
// reconstructed from the normalized portal ID. Older portals without saved
// GUIDs retain the pre-existing lookup behavior.
func (db *chatDB) chatGUIDsForPortal(
	ctx context.Context,
	portal *bridgev2.Portal,
	c *IMClient,
	bundledData any,
	refreshExact bool,
) ([]string, error) {
	var exactGUIDs []string
	bundleSuppliedExactGUIDs := false
	if bundle, ok := bundledData.(chatDBBackfillGUIDBundle); ok && len(bundle.ChatGUIDs) > 0 {
		exactGUIDs = uniqueStrings(bundle.ChatGUIDs)
		bundleSuppliedExactGUIDs = len(exactGUIDs) > 0
	}
	if portal != nil {
		if meta, ok := portal.Metadata.(*PortalMetadata); ok && len(meta.ChatDBGUIDs) > 0 {
			exactGUIDs = appendUniqueStrings(exactGUIDs, meta.ChatDBGUIDs...)
		}
	}
	if len(exactGUIDs) > 0 {
		if refreshExact {
			if bundleSuppliedExactGUIDs {
				// Initial sync and restore-chat build this bundle from the same
				// complete chat.db enumeration that queued the resync. Persist
				// its union without immediately repeating that global scan once
				// per portal.
				changed, err := persistExactChatDBGUIDsForPortal(ctx, portal, exactGUIDs)
				if err != nil {
					return nil, fmt.Errorf("persist bundled exact chat.db GUID metadata: %w", err)
				}
				if changed {
					zerolog.Ctx(ctx).Info().
						Int("guid_count", len(exactGUIDs)).
						Msg("Persisted bundled exact chat.db GUID metadata before backfill")
				}
			} else {
				var err error
				exactGUIDs, err = db.refreshExactChatDBGUIDsForPortal(ctx, portal, exactGUIDs)
				if err != nil {
					return nil, err
				}
			}
		}
		return exactGUIDs, nil
	}

	portalID := ""
	if portal != nil {
		portalID = string(portal.ID)
	}
	if strings.Contains(portalID, ",") {
		chatGUID := db.findGroupChatGUID(portalID, c)
		if chatGUID != "" {
			return []string{chatGUID}, nil
		}
		return nil, nil
	}
	// Contact-aware fallback for portals created before exact GUID metadata
	// was introduced.
	return uniqueStrings(c.getContactChatGUIDs(portalID)), nil
}

// refreshExactChatDBGUIDsForPortal grows an already-authoritative exact set at
// a backfill-session boundary. Only enumerated GUIDs whose normalized identity
// is already represented may be added; contact aliases and guessed suffixes
// are never expanded. Enumeration failures retain the prior set, while save
// failures roll back in-memory metadata and fail the fetch so a retry can
// persist the same union.
func (db *chatDB) refreshExactChatDBGUIDsForPortal(
	ctx context.Context,
	portal *bridgev2.Portal,
	existing []string,
) ([]string, error) {
	merged := uniqueStrings(existing)
	var added []string
	chats, err := db.api.GetChatsWithMessagesAfter(time.Time{})
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to enumerate chat.db exact GUID variants before backfill")
	} else {
		merged, added = refreshedChatDBGUIDs(existing, chats)
	}

	if portal != nil {
		changed, updateErr := persistExactChatDBGUIDsForPortal(ctx, portal, merged)
		if updateErr != nil {
			err = updateErr
			return nil, fmt.Errorf("persist refreshed exact chat.db GUID metadata: %w", err)
		}
		if changed {
			zerolog.Ctx(ctx).Info().
				Int("added_guid_count", len(added)).
				Msg("Persisted exact chat.db GUID metadata before backfill")
		}
	}
	if len(added) > 0 {
		zerolog.Ctx(ctx).Info().
			Int("added_guid_count", len(added)).
			Msg("Refreshed exact chat.db GUID set before backfill")
	}
	return merged, nil
}

func persistExactChatDBGUIDsForPortal(
	ctx context.Context,
	portal *bridgev2.Portal,
	exactGUIDs []string,
) (bool, error) {
	if portal == nil {
		return false, nil
	}
	var save func(context.Context, *bridgev2.Portal) error
	if portal.Bridge != nil {
		save = func(ctx context.Context, portal *bridgev2.Portal) error {
			return portal.Save(ctx)
		}
	}
	return updatePortalChatDBGUIDMetadata(ctx, portal, exactGUIDs, save)
}

// chatDBGUIDIdentityKey removes only representation details that do not change
// the remote conversation identity: the service prefix and Apple's known SMS
// forwarding suffix. It deliberately does not expand through contacts.
func chatDBGUIDIdentityKey(chatGUID string) string {
	parsed := imessage.ParseIdentifier(chatGUID)
	if parsed.LocalID == "" {
		return ""
	}
	if parsed.IsGroup {
		return "group:" + strings.ToLower(stripSmsSuffix(parsed.LocalID))
	}
	portalID := identifierToPortalID(parsed)
	if portalID == "" {
		return ""
	}
	return "dm:" + normalizeIdentifierForPortalID(string(portalID))
}

// refreshedChatDBGUIDs unions exact GUIDs currently enumerated by chat.db into
// an existing authoritative set. Existing entries are never removed, and an
// empty set remains empty so legacy portals retain contact-aware fallback.
func refreshedChatDBGUIDs(existing []string, chats []imessage.ChatIdentifier) (merged, added []string) {
	merged = uniqueStrings(existing)
	if len(merged) == 0 {
		return merged, nil
	}
	identities := make(map[string]struct{}, len(merged))
	seen := make(map[string]struct{}, len(merged))
	for _, chatGUID := range merged {
		seen[chatGUID] = struct{}{}
		if identity := chatDBGUIDIdentityKey(chatGUID); identity != "" {
			identities[identity] = struct{}{}
		}
	}
	for _, chat := range chats {
		chatGUID := chat.ChatGUID
		if chatGUID == "" {
			continue
		}
		if _, exists := seen[chatGUID]; exists {
			continue
		}
		identity := chatDBGUIDIdentityKey(chatGUID)
		if _, matches := identities[identity]; identity == "" || !matches {
			continue
		}
		seen[chatGUID] = struct{}{}
		merged = append(merged, chatGUID)
		added = append(added, chatGUID)
	}
	return merged, added
}

func deduplicateChatDBMessages(messages []*imessage.Message) []*imessage.Message {
	seen := make(map[string]struct{}, len(messages))
	result := make([]*imessage.Message, 0, len(messages))
	for _, message := range messages {
		if message == nil || message.GUID == "" {
			result = append(result, message)
			continue
		}
		if _, ok := seen[message.GUID]; ok {
			continue
		}
		seen[message.GUID] = struct{}{}
		result = append(result, message)
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func chatDBCursorStateForGUID(cursorTimes map[string]chatDBBackfillCursorPosition, chatGUID string, hasBackwardCursor bool) (chatDBBackfillCursorPosition, bool, bool) {
	if cursorPos, ok := cursorTimes[chatGUID]; ok {
		return cursorPos, true, false
	}
	return chatDBBackfillCursorPosition{}, false, hasBackwardCursor
}

func encodeChatDBBackfillCursor(messagesByGUID map[string][]*imessage.Message, count int, forward bool) networkid.PaginationCursor {
	if forward || count <= 0 {
		return ""
	}
	cursor := make(map[string]chatDBBackfillCursorPosition, len(messagesByGUID))
	for chatGUID, messages := range messagesByGUID {
		if len(messages) < count {
			continue
		}
		oldest := messages[0]
		for _, msg := range messages[1:] {
			if msg != nil && (oldest == nil || msg.Time.Before(oldest.Time) || (msg.Time.Equal(oldest.Time) && msg.RowID < oldest.RowID)) {
				oldest = msg
			}
		}
		if oldest == nil || oldest.Time.IsZero() {
			continue
		}
		cursor[chatGUID] = chatDBBackfillCursorPosition{
			TimeNS: oldest.Time.UnixNano(),
			RowID:  oldest.RowID,
		}
	}
	if len(cursor) == 0 {
		return ""
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return networkid.PaginationCursor(encoded)
}

func decodeChatDBBackfillCursor(cursor networkid.PaginationCursor, chatGUIDs []string) map[string]chatDBBackfillCursorPosition {
	times := make(map[string]chatDBBackfillCursorPosition, len(chatGUIDs))
	if cursor == "" {
		return times
	}
	var perGUID map[string]chatDBBackfillCursorPosition
	if err := json.Unmarshal([]byte(cursor), &perGUID); err == nil {
		for chatGUID, pos := range perGUID {
			if pos.TimeNS == 0 {
				continue
			}
			times[chatGUID] = pos
		}
		return times
	}
	var legacyPerGUID map[string]int64
	if err := json.Unmarshal([]byte(cursor), &legacyPerGUID); err == nil {
		for chatGUID, ns := range legacyPerGUID {
			if ns == 0 {
				continue
			}
			times[chatGUID] = chatDBBackfillCursorPosition{TimeNS: ns}
		}
		return times
	}
	ns, err := strconv.ParseInt(string(cursor), 10, 64)
	if err != nil {
		return times
	}
	for _, chatGUID := range chatGUIDs {
		times[chatGUID] = chatDBBackfillCursorPosition{TimeNS: ns}
	}
	return times
}

// ============================================================================
// chat.db ↔ portal ID conversion
// ============================================================================

// portalIDToChatGUIDs converts a DM portal ID to possible chat.db GUIDs.
// Returns multiple possible GUIDs to try, since macOS versions differ:
// Tahoe+ uses "any;-;" while older uses "iMessage;-;" or "SMS;-;".
//
// Note: Group portal IDs (comma-separated) are handled by findGroupChatGUID instead.
func portalIDToChatGUIDs(portalID string) []string {
	// DMs: strip tel:/mailto: prefix and try multiple service prefixes
	localID := stripIdentifierPrefix(portalID)
	if localID == "" {
		return nil
	}
	// Strip legacy (sms...) suffix from pre-fix portal IDs so chat.db GUID
	// candidates match: "tel:+12155167207(smsft)" → localID "+12155167207(smsft)"
	// → stripped "+12155167207", producing "any;-;+12155167207" which matches chat.db.
	localID = stripSmsSuffix(localID)
	return []string{
		"any;-;" + localID,
		"iMessage;-;" + localID,
		"SMS;-;" + localID,
	}
}

// identifierToPortalID converts a chat.db Identifier to a clean portal ID.
func identifierToPortalID(id imessage.Identifier) networkid.PortalID {
	if id.IsGroup {
		return networkid.PortalID(id.String())
	}
	localID := id.LocalID
	// Strip Apple SMS service suffixes: "+12155167207(smsft)" → "+12155167207",
	// "787473(smsft)" → "787473". These are native Apple formats that appear in
	// chat.db for SMS Forwarding service types.
	localID = stripSmsSuffix(localID)
	if strings.HasPrefix(localID, "+") {
		return networkid.PortalID("tel:" + localID)
	}
	if strings.Contains(localID, "@") {
		return networkid.PortalID("mailto:" + localID)
	}
	// Short codes and numeric-only identifiers (e.g., "242733") are SMS-based.
	// Rustpush creates these with "tel:" prefix, so we must match.
	if isNumeric(localID) {
		return networkid.PortalID("tel:" + localID)
	}
	return networkid.PortalID(localID)
}

// ============================================================================
// chat.db message conversion
// ============================================================================

func chatDBMakeEventSender(msg *imessage.Message, c *IMClient) bridgev2.EventSender {
	if msg.IsFromMe {
		return bridgev2.EventSender{
			IsFromMe:    true,
			SenderLogin: c.UserLogin.ID,
			Sender:      makeUserID(c.handle),
		}
	}
	return bridgev2.EventSender{
		IsFromMe: false,
		Sender:   makeUserID(addIdentifierPrefix(stripSmsSuffix(msg.Sender.LocalID))),
	}
}

func normalizeChatDBMessageText(msg *imessage.Message) {
	msg.Text = strings.TrimSpace(strings.ReplaceAll(msg.Text, "\uFFFC", ""))
	msg.Subject = strings.TrimSpace(msg.Subject)
}

func chatDBMessageCanBackfill(msg *imessage.Message) bool {
	if msg == nil || msg.ItemType != imessage.ItemTypeMessage || msg.Tapback != nil {
		return false
	}
	if !msg.IsFromMe && msg.Sender.LocalID == "" {
		return false
	}
	normalizeChatDBMessageText(msg)
	if msg.Text != "" || msg.Subject != "" {
		return true
	}
	for _, att := range msg.Attachments {
		if att != nil {
			return true
		}
	}
	return false
}

func (db *chatDB) hasBackfillableMessages(chatGUID string, maxMessages int) (bool, error) {
	before := time.Now().Add(time.Minute)
	if prober, ok := db.api.(chatDBBackfillableProber); ok {
		return prober.HasBackfillableMessagesBefore(chatGUID, before, maxMessages)
	}

	const uncappedInitialBackfill = 1<<31 - 1
	var (
		messages []*imessage.Message
		err      error
	)
	if maxMessages > 0 && maxMessages < uncappedInitialBackfill {
		messages, err = db.api.GetMessagesBeforeWithLimit(chatGUID, before, maxMessages)
	} else {
		messages, err = db.api.GetMessagesSinceDate(chatGUID, time.Time{}, "")
	}
	if err != nil {
		return false, err
	}
	for _, msg := range messages {
		if chatDBMessageCanBackfill(msg) {
			return true, nil
		}
	}
	return false, nil
}

func convertChatDBMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg *imessage.Message, urlPreviewsInBackfill bool) (*bridgev2.ConvertedMessage, error) {
	normalizeChatDBMessageText(msg)
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    msg.Text,
	}
	if msg.Subject != "" {
		if msg.Text != "" {
			content.Body = fmt.Sprintf("**%s**\n%s", msg.Subject, msg.Text)
			content.Format = event.FormatHTML
			content.FormattedBody = fmt.Sprintf("<strong>%s</strong><br/>%s", msg.Subject, msg.Text)
		} else {
			content.Body = msg.Subject
		}
	}
	if msg.IsEmote {
		content.MsgType = event.MsgEmote
	}

	// URL preview: detect URL and fetch og: metadata + image
	if urlPreviewsInBackfill {
		if detectedURL := urlRegex.FindString(msg.Text); detectedURL != "" {
			content.BeeperLinkPreviews = []*event.BeeperLinkPreview{
				fetchURLPreview(ctx, portal.Bridge, intent, portal.MXID, detectedURL),
			}
		}
	}

	cm := &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventMessage,
			Content: content,
		}},
	}
	if msg.ReplyToGUID != "" {
		cm.ReplyTo = chatDBReplyTarget(msg.ReplyToGUID, msg.ReplyToPart)
	}
	return cm, nil
}

func chatDBAttachmentMessagePartID(guid string, index int, cm *bridgev2.ConvertedMessage) string {
	partID := fmt.Sprintf("%s_att%d", guid, index)
	if chatDBAttachmentIsNotice(cm) {
		return partID + "_notice"
	}
	return partID
}

func chatDBAttachmentIsNotice(cm *bridgev2.ConvertedMessage) bool {
	return cm != nil &&
		len(cm.Parts) == 1 &&
		cm.Parts[0] != nil &&
		cm.Parts[0].Content != nil &&
		cm.Parts[0].Content.MsgType == event.MsgNotice
}

func convertChatDBAttachment(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg *imessage.Message, att *imessage.Attachment, videoTranscoding, heicConversion bool, heicQuality int) (*bridgev2.ConvertedMessage, error) {
	mimeType := att.GetMimeType()
	fileName := att.GetFileName()

	data, err := att.Read()
	if err != nil {
		return chatDBAttachmentNotice(fileName, "read"), nil
	}

	// Convert CAF Opus voice messages to OGG Opus for Matrix/Beeper clients
	var durationMs int
	if mimeType == "audio/x-caf" || strings.HasSuffix(strings.ToLower(fileName), ".caf") {
		data, mimeType, fileName, durationMs = convertAudioForMatrix(data, mimeType, fileName)
	}

	// Remux/transcode non-MP4 videos to MP4 for broad Matrix client compatibility.
	log := zerolog.Ctx(ctx)
	if videoTranscoding && ffmpeg.Supported() && strings.HasPrefix(mimeType, "video/") && mimeType != "video/mp4" {
		origMime := mimeType
		origSize := len(data)
		converted, method, convertErr := transcodeToMP4(ctx, log, data, mimeType)
		if convertErr != nil {
			log.Warn().Err(convertErr).Str("guid", msg.GUID).Str("original_mime", origMime).
				Msg("FFmpeg video conversion failed after retries, uploading original")
		} else {
			log.Info().Str("guid", msg.GUID).Str("original_mime", origMime).
				Str("method", method).Int("original_bytes", origSize).Int("converted_bytes", len(converted)).
				Msg("Video transcoded to MP4")
			data = converted
			mimeType = "video/mp4"
			fileName = strings.TrimSuffix(fileName, filepath.Ext(fileName)) + ".mp4"
		}
	}

	// Convert HEIC/HEIF images to JPEG since most Matrix clients can't display HEIC.
	var heicImg image.Image
	data, mimeType, fileName, heicImg = maybeConvertHEIC(log, data, mimeType, fileName, heicQuality, heicConversion)

	// Extract image dimensions and generate thumbnail
	var imgWidth, imgHeight int
	var thumbData []byte
	var thumbW, thumbH int
	if heicImg != nil {
		b := heicImg.Bounds()
		imgWidth, imgHeight = b.Dx(), b.Dy()
		if imgWidth > 800 || imgHeight > 800 {
			thumbData, thumbW, thumbH = scaleAndEncodeThumb(heicImg, imgWidth, imgHeight)
		}
	} else if strings.HasPrefix(mimeType, "image/") || looksLikeImage(data) {
		if mimeType == "image/gif" {
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
				imgWidth, imgHeight = cfg.Width, cfg.Height
			}
		} else if img, fmtName, _ := decodeImageData(data); img != nil {
			b := img.Bounds()
			imgWidth, imgHeight = b.Dx(), b.Dy()
			// Re-encode TIFF as JPEG for compatibility (PNG is fine as-is)
			if fmtName == "tiff" {
				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err == nil {
					data = buf.Bytes()
					mimeType = "image/jpeg"
					fileName = strings.TrimSuffix(fileName, filepath.Ext(fileName)) + ".jpg"
				}
			}
			if imgWidth > 800 || imgHeight > 800 {
				thumbData, thumbW, thumbH = scaleAndEncodeThumb(img, imgWidth, imgHeight)
			}
		}
	}

	content := &event.MessageEventContent{
		MsgType: mimeToMsgType(mimeType),
		Body:    fileName,
		Info: &event.FileInfo{
			MimeType: mimeType,
			Size:     len(data),
			Width:    imgWidth,
			Height:   imgHeight,
		},
	}

	// Mark as voice message if this was a CAF voice recording
	if durationMs > 0 {
		content.MSC3245Voice = &event.MSC3245Voice{}
		content.MSC1767Audio = &event.MSC1767Audio{
			Duration: durationMs,
		}
	}

	if intent != nil {
		url, encFile, err := intent.UploadMedia(ctx, "", data, fileName, mimeType)
		if err != nil {
			return chatDBAttachmentNotice(fileName, "uploaded"), nil
		}
		if encFile != nil {
			content.File = encFile
		} else {
			content.URL = url
		}

		// Upload image thumbnail
		if thumbData != nil {
			thumbURL, thumbEnc, thumbErr := intent.UploadMedia(ctx, "", thumbData, "thumbnail.jpg", "image/jpeg")
			if thumbErr == nil {
				if thumbEnc != nil {
					content.Info.ThumbnailFile = thumbEnc
				} else {
					content.Info.ThumbnailURL = thumbURL
				}
				content.Info.ThumbnailInfo = &event.FileInfo{
					MimeType: "image/jpeg",
					Size:     len(thumbData),
					Width:    thumbW,
					Height:   thumbH,
				}
			}
		}
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventMessage,
			Content: content,
		}},
	}, nil
}

func chatDBAttachmentNotice(fileName, action string) *bridgev2.ConvertedMessage {
	if strings.TrimSpace(fileName) == "" {
		fileName = "attachment"
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    fmt.Sprintf("Attachment could not be %s (%s).", action, fileName),
			},
		}},
	}
}
