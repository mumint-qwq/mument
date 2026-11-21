# 007-F: original content evidence and shared raster assets

Baseline: aaaefebaf29084dc22fc5f0b03cbaeb73aafdb59, tree
809188ef3ed75864ab5be078d388201ea944be0b. Production remains unchanged.

## Invariants and commit order

C `refs` identify current location/version, `assets` identify a content generation,
`results.digest` identifies encoded thumbnail bytes. F adds separate SHA-256
original proofs; it never interprets a thumbnail digest as original evidence.
No existing table gains columns. All new tables are ignored by E3 v1 writers.

```mermaid
sequenceDiagram
 participant FS as Protected filesystem
 participant F as One F worker
 participant DB as Ledger
 F->>DB: bounded indexed discovery / successful-copy hint
 F->>FS: open configured root + fd, stat exact indexed version
 F->>DB: capture current reference token and durable scope identity
 F->>FS: stream original outside all gates, one hash for all candidates
 F->>FS: fstat, reopen path, compare full version and root/mount
 F->>DB: sorted unique physical stripes → location gate → short transaction
 Note over F,DB: recheck path/version; CAS proof and empty target reference
 F->>DB: attach to proven asset, retain original results/epoch/usage/cooldown
```

Controlled cross-device move: existing rename intent becomes ambiguous on rename
failure; copy and asynchronous source deletion retain existing semantics. Only
successful regular-file copies supply lightweight hints. Actual EXDEV is kept
separate from other rename failures. No original hashing runs in that request.
Source proofs must have been committed before the source disappears. The worker
checks the target from configured roots and the current index before attachment.

External move: periodic bounded index pages discover new locations even if a
hint is lost. Old asset proofs survive missing sources. A new path is hashed only
when it has useful cached results to prove or same-size proven candidates.
An unproven old asset cannot acquire a proof from a new location. Old external
references are retained: E2 cannot prove external unlink from absence alone.

Concurrent replacement: fd remains on old inode; post-read fd and current-path
checks include ctime, size, device and inode. A change rejects work. The final
location-gated check and exact SQL reference CAS reject superseded jobs. A
foreground publication winning before F preserves its own results. Late old
workers cannot publish through a rebound reference token. External filesystem
operations are not atomic with SQL; normal C current-version checks still gate
all subsequent hits.

## Isolation and recovery

Physical stripes are acquired in ascending order, deduplicated, then C location
lock, then SQL transaction. F never reads originals under these locks. Matching
reads existing thumbnail bytes under physical gates and verifies integrity, but
never decodes or updates usage. Active E2/E3 intents cause deferral. The shared
reference insertion serializes with final-reference reclamation. Multi-reference
capacity protection remains E3's existing policy. No historical result merge.

F durable scope pins contain E's filesystem/dev/inode/birth identity, never a
persisted mount ID. Runtime D guards compare current mount IDs, reject symlinks
and nested mounts. Missing durable identity disables the scope. An explicitly
changed configured path creates a new scope binding; no automatic mount mapping.

Pending work is coalesced by scope/path/current index version. Completed state
prevents duplicate hashing. Replaced index versions supersede work. Retries are
bounded; offline and pressure deferrals consume no failure attempt. Pages revisit
current paths after restart, so lost hints are recoverable. Proofs remain per
asset; completed path state is bounded to current/recent observations and pruned
in small pages. A crash during hashing leaves no proof. A crash after SQL commit
is idempotent; no filesystem mutation is performed by F.

## Configuration and budget

`server.previewReuse.enabled` defaults false; `scopes` use D's explicit source,
relative directory and optional mountPoint format, independently of D's switch.
No automatic decoding, TTL or production scope. Start with one worker, pages of
32 (or existing smaller D pages), 64 MiB maximum originals, 256 KiB buffer,
8 MiB/s. Idle/CPU/memory/cache-space checks run between chunks. Cancellation and
shutdown stop acquisition; dependency ownership is retained if draining times out.
Disabling F preserves already valid C shared references. No GET or preview
request waits for F or hashes originals on F's behalf.

Validation and any deviations from this design are recorded separately. A fixed
candidate is not a production authorization or a claim that unrun matrix entries
passed.

## Implemented refinements

`content_ready` is a trigger-maintained auxiliary index over proofs that still
have at least one result. Both cheap size screening and exact digest matching
seek bounded pages; cold/deleted proofs cannot force a scan of history. E3 can
continue to write its original result table while these additive triggers run.
A target proof without a usable match is stored against its own exact token, so
later candidate checks reuse the proof without repeating original I/O.

Immediately around binding, F claims D's existing warmup admission flag. Inside
the location gate it checks active foreground requests/flights, allowing a
foreground generation with an empty target token to win. A new foreground path
plan after this check waits on the location gate and resolves the shared token.
No flight mutex is held across filesystem I/O or SQL. One binding attempt has a
32 MiB total thumbnail-integrity budget across candidate variants. Original
proof I/O precedes these gates. E's last-reference checks and F's insert remain
serialized by the location gate and transaction.

External retirement remains deliberately unimplemented: F never converts
absence to an E2 deletion proof. Missing old refs are retained. All time-of-check
rules rely on the strong filesystem version contract used by C/E (including
nanosecond ctime and physical identity); stat-indistinguishable privileged edits
are outside that contract. The mutation fixture crosses the filesystem's
metadata timestamp update quantum before injecting an in-place edit.

F enables the existing browse-activity signal even when D is disabled. Ordinary
and paged handlers already bracket their work with `TrackWarmupBrowse`; enabling
this shared signal makes F yield for those requests and for the quiet interval
afterward. It does not start D or change directory response generation.

`content_empty` proves that an asset was created after F's additive schema and
has never published a result. Asset/result triggers also execute for old E3
writers. Binding requires this marker and the target token's own durable full
proof, not merely the present absence of result rows. A previously published
asset whose last variant was evicted cannot be rebound to a duplicate asset;
this preserves its cooldown and avoids implicit historical merging. Empty assets
from before F without this provenance stay on their ordinary demand path. No
startup history scan attempts to manufacture unpublished provenance.

## Observability and recovery refinement

`content_origins` is an audit locator for the original exact reference in a
content proof. It contains no second image metadata or generator cache. Inserts
compare the proof origin and still-current reference; a shared target cannot
replace the original locator. A bounded ready-asset keyset page reports whether
the exact origin reference remains, capped reference/unproven counts, and a
configured relative locator. The administrator reopens roots without following
symlinks, compares existing durable pins, and observes file versions without
reading original content. Unresolved paths and unknown mounts stay unconfirmed.
Current source/scope filtering redacts old locations outside the enabled policy.

Work claims distinguish completion, retry backoff and resumed pending state.
Their opaque version also contains the current C asset token, detecting old E3
reference replacement after rollback even when the B observation is unchanged.
SQLite busy/locked is a bounded deferral, not a file I/O failure. Existing root
pins are read-only on the hot chunk checks. Hints removed from configuration or
not yet indexed do not block the queue; index history remains the durable
rediscovery path. Exact proof reuse avoids repeating original reads.

The long mixed test uses two real private tmpfs mounts, concurrent D/F workers,
foreground browsing, external copies/renames, real E2 removal windows, controlled
memory-pressure signals, wrong-volume substitution and same-volume remounts.
Only isolated eligibility timestamps are aged to make E2 grace testable; no real
production grace is shortened. Synthetic cgroup inputs do not prove actual
container resource enforcement. Real process exits separately cover claiming,
complete hash before SQL, and committed binding before work cleanup.
