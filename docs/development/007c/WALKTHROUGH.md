# 007-C code walkthrough

1. `previewHandler` checks the active source and view permission. The public
   route gets its file context from the share middleware. `PreviewHelperFunc`
   can send originals directly. For a generated static preview,
   `planLedgerPreview` observes the authorized file and reserves its content
   generation with `Repository.Resolve`.
2. `loadPreviewCache` reads a ready result for the exact reference and
   variant. It rejects a mismatched length or digest and compares the live
   file version again. A miss goes through the existing A flight and bounded
   decode path. `generateAndPublish` checks the descriptor and the pathname,
   stores complete bytes, then publishes the result using the pinned
   reference. Original bytes are handled explicitly.
3. `ResourcePatchHandler` validates each item and flushes independent moves
   in groups. `prepareCacheMoves` only journals already observed references.
   `renameResources` records explicit rename success and the target stat.
   `CompleteRenames` changes references in a group transaction. The same
   assets and physical previews are reused. `IndexImpactBatch` still
   consolidates source and destination refresh work.
4. The replaced target's old reference cannot match the new file.
   `TransferMovedImage` copies a B result only from the prior source version
   and attaches it with the actual new version and guard. A scanner result
   for the replaced target no longer has a valid guard. For directory moves
   the cache locator follows the same physical parent. The index maintenance
   queue checks and copies ready descendants in bounded pages before it
   deletes the old tree rows.
5. A process can lose power after journaling, after rename, or after SQLite
   commits. `RecoverBefore` retires incomplete intents with primary-key
   version checks. A completed commit remains usable when the target still
   matches. When the outcome cannot be proved, the next preview regenerates.
   Recovery leaves filesystem operations and later edits alone.
6. Old cache files stay on disk. C only accepts bytes tied to a recorded
   complete observation and verified digest. Reopening with A/B uses the
   historical keys. Returning to C rechecks current versions and content
   when reading each recorded result. An amended thumbnail key or changed
   original is rejected.
