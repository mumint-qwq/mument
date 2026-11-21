# One image through five paths

Read together with [design](DESIGN.md), [operations](OPERATIONS.md), and
[validation](VALIDATION.md). Original proof, current file reference and physical
preview publication remain separate. F never serves a preview directly.

1. **Controlled cross-device move after proof.** A foreground authorized preview
   creates C asset A and result variants via the existing generator. F sees its
   exact indexed version V1, opens through `guardedFile`, hashes one fd with
   `cacheledger.HashContent`, reopens/rechecks root/path, and `ProveContent` CAS
   stores SHA-256 against A/V1. `files.moveResources` retains C rename preparation.
   On actual EXDEV it releases the location gate, performs existing `MoveByCopy`
   and its asynchronous source removal, then `HintCopiedContent` records only
   the successful regular target version, with EXDEV distinguished. No hash is
   in the request. F hints/history discover V2 at the new target. Full target
   hash equals the immutable proof. Sorted deduplicated physical stripes cover
   existing result keys; thumbnail byte integrity is checked outside the location
   gate. The location gate then protects a final guarded stat and `BindContent`
   transaction. The target must have its own durable equal full proof and a
   `content_empty` marker proving that this token has never published. A token
   emptied by eviction cannot be adopted. The eligible target token becomes A.
   Source references are not deleted by F. Result key, epoch, generation time, use time and cooldown stay
   unchanged. A copy failure supplies no hint; a crash before hint is repaired
   by pagination. A crash during hashing supplies no proof.

2. **External move and scan.** The index commits a new path/version through B's
   existing scanner. F's manual-style keyset page consumes this persisted row,
   independently of D and without altering D completion guards. The same stream
   and CAS protocol proves the new location. `refs.source` is C's physical parent
   namespace, not an authorization identity or an original digest. Same-directory
   rename creates a new leaf reference; ancestor directory rename can preserve
   C's existing parent locator fast path. Old external missing references remain
   protected because F cannot prove unlink. Administrator retention pages expose
   bounded source-relative observations; missing paths remain unconfirmed.
   Lost notifications and process exit
   are handled by periodic pagination; completed current observations avoid
   duplicate hashing. Work keys include the current C token, so an E3 writer
   changing C without changing B cannot hide a new generation. A changed
   configured root is rejected by durable pins.

3. **Insufficient proof and normal demand.** If A's old source vanished before
   F ever read V1, hashing the target cannot establish what A originally decoded.
   Size/name/inode are only filters. The target's ordinary authorized C/A preview
   can generate a fresh asset. F may prove that asset from the target's exact
   current fd, but never backdate the evidence onto A. Small original direct
   responses remain direct and do not count as reused raster cache. If the
   foreground publishes during F's hash, its result wins; F records only its
   own proven generation and does not merge historical assets.

4. **Two copies share, one disappears.** Each current path/version has its own
   reference after verification, both pointing to A. Actual preview retrieval
   still checks the target's current version, variant and encoded digest and
   records real foreground use via C's coalesced touch. Background binding does
   not touch usage. Independently confirmed application deletion removes only
   the exact old reference through E2. The other reference protects the result.
   Last independently confirmed unlink enters E2's original grace. A hardlink
   write or unlink changes ctime and requires fresh current-version verification;
   there is no inode-only shortcut. Reappearing old paths use normal C Resolve.

5. **F versus physical reclamation.** Both use physical key gates before the C
   location gate and a short transaction. All stripes for one F candidate are
   sorted and deduplicated to avoid inverse acquisition or self-deadlock. Long
   original reads happen before these gates. If F inserts first, E sees a live
   reference and cannot reclaim as orphan. If an E2/E3 intent already owns any
   result key, `ContentPending`/transaction checks defer attachment; no isolation
   path is read or rewritten. An already removed result falls back to demand.
   If the physical publication changed, the witness token fails CAS. SQL busy,
   stale references and superseding foreground publishers leave current files
   alone. Store-before-SQL leftovers remain subject to E's conservative policy;
   F never deletes them. Multiple refs and unproven source losses retain E3
   protection. Shutdown cancels F before preview/ledger/index closure, and an
   undrained worker retains dependencies.

Permission is always evaluated against the requested current source/path/share
by the existing HTTP layer. No endpoint accepts a digest/asset/path for reuse.
Only administrator configuration selects background scopes. Aggregate status
contains no file identities. The candidate has a single background generator
owner (the existing preview service); F is a verifier and reference writer.
