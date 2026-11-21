# 007-F candidate configuration

This branch is an isolated development candidate. The task handoff records production as 007-B; Docker runtime was not independently
verified here. No deployment, enablement, real gallery testing, push or merge is performed here.

```yaml
server:
  cacheDir: /explicit/persistent/test-cache
  cacheDirCleanup: false
  previewReuse:
    enabled: false
    scopes:
      - source: photos
        directory: Pictures
        # mountPoint: .  # only if the source root really is a mount point
```

The frozen keys are `server.previewReuse.enabled` and `.scopes`, with scope
`source`, `directory`, optional `mountPoint` (the same grammar as D). Settings
require an indexed source, retained index/cache, 1–16 nonoverlapping scopes and
clean relative component paths. Source names resolve through configured source
roots. This switch is independent of `previewWarmup`, orphan reclamation and
capacity eviction. Scope files must already have B observations in the index.
JPEG and PNG are discovered. Other formats keep existing behavior.

One process-local worker uses existing 16-row keyset pages (smaller than the
suggested 32), a 256 KiB buffer, 64 MiB maximum originals, and 8 MiB/s pacing.
These constants are fixed in this candidate; no production values are assumed.
The worker checks foreground activity, D's CPU/memory probe, cache space and
scope identity before and between chunks. D starting a decode makes F defer.
Large originals are skipped. An original shortcut has no cached raster result
to reuse. A source already gone without an exact prior proof cannot be recovered
by attaching its old preview to new bytes. Two same-size files need full SHA-256
agreement. EXIF bytes are included.

Administrator-only `GET /api/settings/preview-reuse` returns aggregate reasons,
counts and original read bytes, without paths/digests/assets. POST accepts only
`{"action":"pause"}` or `{"action":"resume"}`. Pause is checked at chunk
boundaries; it stops new claims and defers active work. Configuration changes
require a controlled restart. Do not enable the configuration through ordinary
or public endpoints. Existing preview authorization and share scope checks run
before every preview plan, including shared assets.

First access can race ahead of proof coverage; it follows normal C/A behavior.
F keeps a winning foreground result. After a mismatch or a missing usable
publication, the exact target proof can be reused on a later pass without
rereading the original. Resource/offline deferrals do not consume the three
per-version I/O failure attempts. Work is coalesced by scope/path/index version;
old path state expires after 30 days in 32-row maintenance batches. Copy hints
are capped at 4096, processed in pages of 16, and expire after a day. Lost hints
are rediscovered through index pagination. Proofs are retained per asset for
future moves, independently of path work history. There is no original deletion,
historical asset merge, whole-disk watcher, decode, age TTL, or cache cleanup in F.

Durable scope and cache roots use E's filesystem/dev/inode/birth proof. Unsupported
filesystems or missing proof disable automatic reuse. Runtime mount IDs constrain
an individual operation; persisted IDs do not prevent a verified root from being
reopened in a new namespace/container. Root replacement is rejected. A deliberate
container path remapping requires updated explicit configuration and a verified
new scope binding; this candidate provides no API to erase identity pins.

External missing paths remain unproven references. Absence, offline disks,
permission errors and a scan are not confirmed unlink events. They can leave
cache protected indefinitely. E2's confirmed application deletion is separate.
Multi-reference assets remain protected by E3's existing single-reference policy.
Operators must account for this retention before enabling F at scale.

To stop: pause F, stop the isolated service and let its worker drain; retain
ledger, cache and index. A five-second drain deadline preserves dependency
ownership when a blocked filesystem call outlives shutdown. Setting enabled to
false after restart stops F work but keeps valid C references. Rollback is to
E3 for this candidate, with all additive tables and existing artifacts retained.
Do not reset SQLite or copy production B data into the fixture. See
[validation](VALIDATION.md) for the actual binary sequence and its limits.

The status counters are cumulative observations, not unique image counts.
`discovered` counts bounded index rows visited; `candidateRows` counts size-filter
rows returned; `candidateChecks` counts exact-digest assets inspected for usable
publications. `proofHits` means an existing exact proof avoided another original
read, while `resumedPending` counts reacquired pending work, including retries.
`publicationWins` counts a foreground or D publication observed after hashing;
it does not guess which producer won. `physicalHits` is background integrity
validation of a ready preview; real frontend consumption remains in C's hit/use
metrics. `previewBytes` is the thumbnail validation budget charged, not a claim
about physical disk traffic. `readBytes` records original bytes actually read,
including cancelled/changed attempts. No counter updates cache heat.

Administrator `GET /api/settings/preview-reuse?referencesAfter=` requests a
read-only page of at most 16 proven ready assets. Follow its opaque `next`
cursor to continue. Samples identify configured sources and source-relative old
paths, with `live_verified_observation`, `missing_or_unreadable_unconfirmed`,
`changed_unconfirmed`, `scope_offline`, `scope_identity_unavailable`, or
`origin_ref_superseded`. Outside current configured scopes, paths are redacted.
A reference or unproven-loss count of 17 is a saturated lower bound (`capped`),
not an exact count. Pages are observations at query time, not one global atomic
inventory. Neither absence nor a stale locator authorizes reference retirement;
this audit never deletes, detaches or confirms a loss. Normal status and logs
remain aggregate and contain no paths, original digests or asset identifiers.

The F work key includes the current C token as well as B's index version. A
legacy E3 writer that replaces a reference without changing the B observation
therefore cannot cause F to skip an unproven new generation. A completed proof
can still be reused without rereading originals. Persisted hints outside current
scopes are removed; not-yet-indexed targets fall back to history rediscovery so
an unavailable hint cannot indefinitely block later high-priority hints.
