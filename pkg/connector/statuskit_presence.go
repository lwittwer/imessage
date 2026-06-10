package connector

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
)

const (
	statusKitPresencePendingKeyPrefix    = "statuskit.presence.pending."
	statusKitPresencePortalKeyPrefix     = "statuskit.presence.portal."
	statusKitPresencePortalSeenKeyPrefix = "statuskit.presence.portal_seen."
	statusKitRoomNameRepairKeyPrefix     = "statuskit.presence.room_name_repair."
	statusKitPresenceStateVersionKey     = database.Key("statuskit.presence_state.version")
	statusKitPresenceStateVersion        = "5"
	statusKitRoomNameRepairValuePrefix   = "available_name_v2:"
	statusKitPendingTTL                  = time.Hour
)

type statusKitPendingPresence struct {
	Mode       string    `json:"mode"`
	ObservedAt time.Time `json:"observed_at"`
}

func statusKitModeKey(mode *string, available bool) string {
	if available {
		return "available"
	}
	if mode != nil && *mode != "" {
		return *mode
	}
	return "unavailable"
}

func statusKitModeSilenced(modeKey string) bool {
	return modeKey != "" && modeKey != "available"
}

func statusKitRoomNameRepairValue(name string) string {
	return statusKitRoomNameRepairValuePrefix + name
}

func encodeStatusKitPendingPresence(modeKey string, observedAt time.Time) string {
	raw, _ := json.Marshal(statusKitPendingPresence{
		Mode:       modeKey,
		ObservedAt: observedAt.UTC(),
	})
	return string(raw)
}

func decodeStatusKitPendingPresence(raw string) (statusKitPendingPresence, bool) {
	if raw == "" {
		return statusKitPendingPresence{}, false
	}
	var pending statusKitPendingPresence
	if err := json.Unmarshal([]byte(raw), &pending); err != nil {
		return statusKitPendingPresence{}, false
	}
	if pending.Mode == "" || pending.ObservedAt.IsZero() {
		return statusKitPendingPresence{}, false
	}
	return pending, true
}

func statusKitPendingExpired(pending statusKitPendingPresence, now time.Time) bool {
	return now.Sub(pending.ObservedAt) > statusKitPendingTTL
}

func statusKitCanonicalAlias(alias string) string {
	return normalizeIdentifierForPortalID(alias)
}

func statusKitAliasVariants(alias string) []string {
	variants := make([]string, 0, 4)
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		for _, existing := range variants {
			if existing == v {
				return
			}
		}
		variants = append(variants, v)
	}

	add(alias)
	normalized := normalizeIdentifierForPortalID(alias)
	add(normalized)
	if strings.HasPrefix(normalized, "tel:") {
		add(strings.TrimPrefix(normalized, "tel:"))
	} else if strings.HasPrefix(normalized, "mailto:") {
		add(strings.TrimPrefix(normalized, "mailto:"))
	}
	return variants
}

func statusKitPendingKey(alias string) database.Key {
	return database.Key(statusKitPresencePendingKeyPrefix + statusKitCanonicalAlias(alias))
}

func statusKitPortalSeenKey(portalID networkid.PortalID) database.Key {
	return database.Key(statusKitPresencePortalSeenKeyPrefix + string(portalID))
}

func statusKitRoomNameRepairKey(portalID networkid.PortalID) database.Key {
	return database.Key(statusKitRoomNameRepairKeyPrefix + string(portalID))
}

func (c *IMClient) getStatusKitPortalSeen(ctx context.Context, portalID networkid.PortalID) time.Time {
	if v, ok := c.statusKitPresenceSeenByPortal.Load(portalID); ok {
		if ts, ok := v.(time.Time); ok {
			return ts
		}
	}
	raw := c.Main.Bridge.DB.KV.Get(ctx, statusKitPortalSeenKey(portalID))
	if raw == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	c.statusKitPresenceSeenByPortal.Store(portalID, ts)
	return ts
}

func (c *IMClient) migrateStatusKitPresenceState(ctx context.Context, log zerolog.Logger) {
	if c == nil || c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.DB == nil {
		return
	}
	if c.Main.Bridge.DB.KV.Get(ctx, statusKitPresenceStateVersionKey) == statusKitPresenceStateVersion {
		return
	}

	rows, err := c.Main.Bridge.DB.RawDB.QueryContext(ctx,
		"SELECT key, value FROM kv_store WHERE bridge_id=$1 AND key LIKE $2 AND value <> '' AND value <> 'available'",
		c.Main.Bridge.ID, statusKitPresencePortalKeyPrefix+"%")
	if err != nil {
		log.Warn().Err(err).Msg("StatusKit presence migration: failed to query old state")
		return
	}

	now := time.Now()
	changed := make(map[networkid.PortalID]struct{})
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		portalID := networkid.PortalID(strings.TrimPrefix(key, statusKitPresencePortalKeyPrefix))
		if portalID == "" {
			continue
		}
		changed[portalID] = struct{}{}
		c.statusKitPresenceByPortal.Store(portalID, "available")
		c.statusKitPresenceSeenByPortal.Store(portalID, now)
		c.Main.Bridge.DB.KV.Set(ctx, database.Key(statusKitPresencePortalKeyPrefix+string(portalID)), "available")
		c.Main.Bridge.DB.KV.Set(ctx, statusKitPortalSeenKey(portalID), now.UTC().Format(time.RFC3339Nano))
		for _, variant := range statusKitAliasVariants(string(portalID)) {
			c.Main.Bridge.DB.KV.Set(ctx, database.Key("statuskit.presence."+variant), "available")
		}
	}
	if err := rows.Err(); err != nil {
		log.Warn().Err(err).Msg("StatusKit presence migration: row iteration failed")
	}
	if err := rows.Close(); err != nil {
		log.Warn().Err(err).Msg("StatusKit presence migration: failed to close query")
	}

	if c.UserLogin == nil {
		log.Info().Int("cleared_modes", len(changed)).Msg("StatusKit presence migration: deferred title repair until login is ready")
		return
	}
	releasedTitles := 0
	for portalID := range changed {
		portal := c.findPortalByID(ctx, portalID)
		if portal == nil || portal.MXID == "" || portal.RoomType != database.RoomTypeDM {
			continue
		}
		if !portal.NameIsCustom && !strings.HasSuffix(portal.Name, "🌙") {
			continue
		}
		nameField := c.dmFocusName(ctx, portal)
		c.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventChatInfoChange,
				PortalKey: networkid.PortalKey{
					ID:       portal.ID,
					Receiver: c.UserLogin.ID,
				},
				LogContext: func(lc zerolog.Context) zerolog.Context {
					return lc.Str("portal_mxid", string(portal.MXID)).
						Str("source", "statuskit_presence_migration")
				},
			},
			ChatInfoChange: &bridgev2.ChatInfoChange{
				ChatInfo: &bridgev2.ChatInfo{
					Name:                       nameField,
					ExcludeChangesFromTimeline: true,
				},
			},
		})
		releasedTitles++
	}
	repairedTitles := c.repairAvailableStatusKitDMRoomNames(ctx, log)

	c.Main.Bridge.DB.KV.Set(ctx, statusKitPresenceStateVersionKey, statusKitPresenceStateVersion)
	if len(changed) > 0 || releasedTitles > 0 || repairedTitles > 0 {
		log.Info().
			Int("cleared_modes", len(changed)).
			Int("released_titles", releasedTitles).
			Int("repaired_titles", repairedTitles).
			Msg("StatusKit presence migration: cleared pre-normalization active states")
	}
}

func (c *IMClient) repairAvailableStatusKitDMRoomNames(ctx context.Context, log zerolog.Logger) int {
	rows, err := c.Main.Bridge.DB.RawDB.QueryContext(ctx,
		"SELECT key FROM kv_store WHERE bridge_id=$1 AND key LIKE $2 AND value = 'available'",
		c.Main.Bridge.ID, statusKitPresencePortalKeyPrefix+"%")
	if err != nil {
		log.Warn().Err(err).Msg("StatusKit room-name repair: failed to query available state")
		return 0
	}

	var portalIDs []networkid.PortalID
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			continue
		}
		portalID := networkid.PortalID(strings.TrimPrefix(key, statusKitPresencePortalKeyPrefix))
		if portalID != "" {
			portalIDs = append(portalIDs, portalID)
		}
	}
	if err := rows.Close(); err != nil {
		log.Warn().Err(err).Msg("StatusKit room-name repair: failed to close query")
	}
	if err := rows.Err(); err != nil {
		log.Warn().Err(err).Msg("StatusKit room-name repair: row iteration failed")
	}

	repaired := 0
	for _, portalID := range portalIDs {
		portal := c.findPortalByID(ctx, portalID)
		if name, ok := c.forceStampAvailableStatusKitDMRoomName(ctx, log, portal); ok {
			c.Main.Bridge.DB.KV.Set(ctx, statusKitRoomNameRepairKey(portal.ID), statusKitRoomNameRepairValue(name))
			repaired++
		}
	}
	return repaired
}

func (c *IMClient) forceStampAvailableStatusKitDMRoomName(ctx context.Context, log zerolog.Logger, portal *bridgev2.Portal) (string, bool) {
	if portal == nil || portal.MXID == "" || portal.RoomType != database.RoomTypeDM || portal.NameIsCustom {
		return "", false
	}
	if c.isMyHandle(string(portal.ID)) || c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.Bot == nil {
		return "", false
	}
	name := c.dmBaseName(ctx, portal)
	if name == "" {
		return "", false
	}
	_, err := c.Main.Bridge.Bot.SendState(ctx, portal.MXID, event.StateRoomName, "", &event.Content{
		Parsed: &event.RoomNameEventContent{Name: name},
		Raw: map[string]any{
			"com.beeper.exclude_from_timeline": true,
			"fi.mau.implicit_name":             true,
		},
	}, time.Now())
	if err != nil {
		log.Warn().Err(err).Str("portal_mxid", string(portal.MXID)).Msg("StatusKit: failed to force-stamp available DM title")
		return "", false
	}
	log.Info().Str("portal_mxid", string(portal.MXID)).Msg("StatusKit: force-stamped available DM title")
	return name, true
}

func (c *IMClient) storePendingStatusKitPresence(ctx context.Context, log zerolog.Logger, alias, modeKey string, observedAt time.Time) {
	canonical := statusKitCanonicalAlias(alias)
	if canonical == "" {
		return
	}
	key := statusKitPendingKey(canonical)
	if existing, ok := decodeStatusKitPendingPresence(c.Main.Bridge.DB.KV.Get(ctx, key)); ok && existing.ObservedAt.After(observedAt) {
		return
	}
	c.Main.Bridge.DB.KV.Set(ctx, key, encodeStatusKitPendingPresence(modeKey, observedAt))
	log.Info().Str("alias", logSafeHandle(canonical)).Str("mode", modeKey).Time("observed_at", observedAt).Msg("StatusKit: stored unresolved pending presence")
}

func (c *IMClient) replayPendingStatusKitPresence(ctx context.Context, log zerolog.Logger, alias string, portalID networkid.PortalID) {
	if portalID == "" {
		return
	}
	now := time.Now()
	var newest statusKitPendingPresence
	var newestKey database.Key
	for _, variant := range statusKitAliasVariants(alias) {
		key := statusKitPendingKey(variant)
		pending, ok := decodeStatusKitPendingPresence(c.Main.Bridge.DB.KV.Get(ctx, key))
		if !ok || statusKitPendingExpired(pending, now) {
			c.Main.Bridge.DB.KV.Set(ctx, key, "")
			continue
		}
		if newest.ObservedAt.IsZero() || pending.ObservedAt.After(newest.ObservedAt) {
			newest = pending
			newestKey = key
		}
	}
	if newest.ObservedAt.IsZero() {
		return
	}
	portal := c.findPortalByID(ctx, portalID)
	if portal == nil {
		return
	}
	if c.applyStatusKitPresenceToPortal(ctx, log, alias, portal, newest.Mode, newest.ObservedAt) {
		c.Main.Bridge.DB.KV.Set(ctx, newestKey, "")
	}
}

func (c *IMClient) sweepStatusKitPresenceState(ctx context.Context, log zerolog.Logger) {
	rows, err := c.Main.Bridge.DB.RawDB.QueryContext(ctx,
		"SELECT key, value FROM kv_store WHERE bridge_id=$1 AND key LIKE $2 AND value <> ''",
		c.Main.Bridge.ID, "statuskit.presence.%")
	if err != nil {
		log.Warn().Err(err).Msg("StatusKit presence sweep: KV query failed")
		return
	}
	defer rows.Close()

	now := time.Now()
	clearedPending := 0
	clearedLegacy := 0
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(key, statusKitPresencePendingKeyPrefix):
			pending, ok := decodeStatusKitPendingPresence(value)
			if !ok || statusKitPendingExpired(pending, now) {
				c.Main.Bridge.DB.KV.Set(ctx, database.Key(key), "")
				clearedPending++
			}
		case strings.HasPrefix(key, statusKitPresencePortalKeyPrefix),
			strings.HasPrefix(key, statusKitPresencePortalSeenKeyPrefix):
			continue
		default:
			alias := strings.TrimPrefix(key, "statuskit.presence.")
			hasMapping := false
			for _, variant := range statusKitAliasVariants(alias) {
				if c.Main.Bridge.DB.KV.Get(ctx, database.Key(statusKitAliasPortalKeyPrefix+statusKitCanonicalAlias(variant))) != "" {
					hasMapping = true
					break
				}
			}
			if !hasMapping {
				c.Main.Bridge.DB.KV.Set(ctx, database.Key(key), "")
				c.statusKitPresence.Delete(alias)
				clearedLegacy++
			}
		}
	}
	if err := rows.Err(); err != nil {
		log.Warn().Err(err).Msg("StatusKit presence sweep: row iteration failed")
	}
	if clearedPending > 0 || clearedLegacy > 0 {
		log.Info().Int("pending", clearedPending).Int("legacy", clearedLegacy).Msg("StatusKit presence sweep: cleared stale rows")
	}
}

func (c *IMClient) periodicStatusKitPresenceSweep(log zerolog.Logger) {
	c.sweepStatusKitPresenceState(context.Background(), log)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.sweepStatusKitPresenceState(context.Background(), log)
		case <-c.stopChan:
			return
		}
	}
}
