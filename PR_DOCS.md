# Upstream PR Plan

Split into 4 PRs, ordered by dependency (PR 1 should merge first since PR 2 touches the same read-state code path).

---

## PR 1: Fix iCloud backfill marking most conversations as unread

**Commits (8):**
```
d564282 Fix iCloud backfill marking most conversations as unread
926c7ab Fix false-positive read marking for portals without cloud_chat rows
6cdc72c Fix synthetic row bypass and align filtering semantics in read-state check
2f56f5d Add deleted=FALSE filter to filtered-row EXISTS query
092f308 Simplify getConversationReadByMe to single COUNT/SUM query
62a0a4f Fix NULL scan bug and simplify to single filtered COUNT
c37baad fix: init-db fails loudly; read receipt uses message direction
526919c fix: stable tiebreaker for same-timestamp messages in getConversationReadByMe
```

**Title:** `fix: iCloud backfill read-state marking`

**Body:**
```markdown
## Summary

- Fix `getConversationReadByMe()` which was marking nearly all CloudKit-backfilled
  conversations as unread. The root cause was a heuristic that only marked conversations
  read if the last message was `is_from_me=true`, missing the common case where the user
  read an incoming message but didn't reply.
- The final implementation uses a two-tier approach: check the direction of the most
  recent non-tapback message (primary), then fall back to checking for non-filtered
  `cloud_chat` rows when no messages are cached yet.
- Along the way: fix NULL scan from `SUM()` on empty result sets, add missing
  `deleted=FALSE` filter, add stable `rowid DESC` tiebreaker for same-timestamp
  messages, and fix synthetic row bypass where `markForwardBackfillDone()` was
  inserting rows before read-state was computed.
- Also makes `init-db` fail loudly instead of silently swallowing errors in the
  install script.

## Test plan

- [ ] Run CloudKit backfill on a fresh bridge and verify conversations where you sent
      the last message are marked read
- [ ] Verify conversations where someone else sent the last message are left unread
- [ ] Verify filtered/junk conversations stay unread
- [ ] Verify DM portals without cloud_chat rows don't get false-positive read marking
```

---

## PR 2: Deduplicate tapback and edit events from APNs re-delivery

**Commits (3):**
```
d31f2ad connector: deduplicate tapback and edit events from APNs re-delivery
9dcc631 connector: fix unread-state regression in tapback/edit dedup
96f975c connector: remove IsStoredMessage check from handleEdit
```

**Title:** `fix: deduplicate tapback events from APNs re-delivery`

**Body:**
```markdown
## Summary

- APNs re-delivers buffered messages on reconnect, and tapbacks can also arrive via
  both CloudKit backfill and APNs, producing duplicate reactions in Matrix rooms.
- Add UUID-based deduplication for tapbacks using `persistTapbackUUID()`, which stores
  the tapback type alongside the UUID to avoid corrupting the read-state query (which
  filters on `tapback_type IS NULL`).
- Remove the `IsStoredMessage` guard from `handleEdit()` — this flag means "Apple
  buffered this within 30s of reconnect", not "we've already processed this". The old
  check was silently discarding legitimate edits that occurred while the bridge was
  offline. Edits are idempotent (`m.replace`), so accepting a potential duplicate is
  preferable to losing user content.

## Test plan

- [ ] Disconnect the bridge, send several tapbacks from another device, reconnect —
      verify no duplicate reactions appear
- [ ] Disconnect, edit a message from another device, reconnect — verify the edit
      is applied (not silently dropped)
- [ ] Verify read-state is not affected by tapback dedup rows
```

---

## PR 3: Download CloudKit group photos for avatars at startup

**Commits (2):**
```
37beb24 Add CloudKit group photo download to populate avatars at startup
1aa0ded Fix warmup goroutine lifecycle and dead photoRef field (PR #12 review)
```

**Title:** `feat: populate group chat avatars from CloudKit photos`

**Body:**
```markdown
## Summary

- Add CloudKit group photo download to populate group chat avatars that aren't
  cached locally. Two code paths: a lazy fallback in `GetChatInfo()` on cache miss,
  and a proactive background warmup goroutine in `ingestCloudChats()` that
  pre-downloads photos after each batch sync.
- Fix goroutine lifecycle: the warmup goroutine now respects client shutdown via
  `stopChan` instead of using `context.Background()`. Also passes `context.Context`
  through to the FFI download call so it's interruptible.
- Remove unused `recordName` field from local `photoRef` struct.

## Test plan

- [ ] Start bridge with group chats that have custom photos — verify avatars appear
      in Matrix rooms
- [ ] Stop the bridge during warmup — verify no goroutine leaks or hangs
- [ ] Verify group chats without photos don't produce errors
```

---

## PR 4: Reliability improvements and build fixes

**Commits (4):**
```
6eb6762 fix: resolve multi-handle ghost display names in Element Web
e249338 fix: log errors in reply parsing, file cleanup, and thumbnail upload
b836342 connector: log previously-silent failures for reliability
b4d9ef2 fix: install libheif unconditionally as required build dep
```

**Title:** `fix: ghost display names, error logging, and libheif build dep`

**Body:**
```markdown
## Summary

- **Ghost display names**: Fix multi-handle contacts (e.g. same person with both
  tel: and mailto: handles) showing as raw Matrix user IDs in Element Web. Root causes:
  missing `bridge_id` filter on ghost query, stale-skip logic preventing re-sends of
  `m.room.member` events, and phantom sender ghosts from CloudKit backfill not getting
  refreshed. Also adds a post-backfill ghost name refresh pass.
- **Error logging**: Add warn/error logging at ~12 sites across `client.go`,
  `mac/messages.go`, and `mac/send.go` where failures were silently swallowed —
  JSON unmarshal errors, `persistMessageUUID` failures, `UserLogin.Save()` errors,
  temp directory cleanup, thumbnail uploads, and reply parsing.
- **Build fix**: Install libheif unconditionally in the Linux install script since
  it's a required compile-time dependency regardless of the HEIC conversion config
  setting.

## Test plan

- [ ] Verify contacts with multiple handles (phone + email) show correct display
      names in Element Web after backfill
- [ ] Run bridge with debug logging and verify previously-silent failures now
      appear in logs
- [ ] Run `scripts/install-linux.sh` declining HEIC conversion — verify build
      still succeeds (libheif installed regardless)
```
