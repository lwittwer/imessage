# TODO

## Scope per-message tombstones to their thread for reused SMS UUIDs

Apple's SMS relay can reuse a message UUID across short-code threads.
`cloud_message` keys on `(login_id, guid)`, and the CloudKit upsert rewrites
`portal_id`, so both messages share one row. When thread B's CloudKit record
shares a UUID with a message unsent in thread A, B's row inherits A's tombstone
(`deleted` is sticky on conflict). B's message is then hidden from chat.db
backfill and restore-chat, and A no longer has a tombstone in its own portal.

`deletedMessageGUIDsInPortals` scopes the lookup by portal, but that only holds
while the row's `portal_id` matches the thread the unsend came from. A full fix
needs a per-portal key for tombstones, for example a separate tombstone table
keyed by `(login_id, guid, portal_id)`.

Raised in the PR 85 review. Rare: it needs a reused UUID and a CloudKit record
for the other thread. Matrix redactions are refused for SMS conversations, so
only Apple-side deletes and unsends can create these tombstones.

Other write paths with the same shared-row problem, to cover with the same fix:

- The recycle-bin seed upsert in `recoverMessagesFromRecycleBin` keeps a
  `message_deleted` row deleted but rewrites its `portal_id` to the portal being
  restored, moving the tombstone out of its thread.
