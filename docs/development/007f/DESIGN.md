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
