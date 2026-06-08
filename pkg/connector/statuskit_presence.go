package connector

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	statusKitPresencePendingKeyPrefix    = "statuskit.presence.pending."
	statusKitPresencePortalKeyPrefix     = "statuskit.presence.portal."
	statusKitPresencePortalSeenKeyPrefix = "statuskit.presence.portal_seen."
	statusKitPendingTTL                  = time.Hour
)

type statusKitPendingPresence struct {
	Mode       string    `json:"mode"`
	ObservedAt time.Time `json:"observed_at"`
}

func statusKitModeKey(mode *string) string {
	if mode != nil && *mode != "" {
		return *mode
	}
	return "available"
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
