# 007-A/B candidate migration

Status: development branch only; not merged into `main`, not deployed, and not
user-accepted as a mument version. 007-C implementation has not started.

## Source and scope

| Stage | Source candidate | Original commit |
| --- | --- | --- |
| Accepted base | Local Quantum main | `72812feeb03b7cac51734e36ca35e05b1b8964d5` |
| 007-A | Preview resource protection RC4 | `a40b2288` |
| 007-B | Image metadata versioning RC3 | `d4cde7a1908b66c0645a34a57c3c0f0d8f45e868` |

The mument starting point is `0d1b0da`. A is an ancestor of B. Backend changes
were applied in two patches (accepted base to A, then A to B), without importing
the previous repository's Git history, private deployment tooling, or logs.

- A adds a shared preview decode memory budget, bounded admission/queue handling,
  same-key generation coalescing, source-change checks, and coordinated shutdown.
  `server.previewMemoryBudgetMB` defaults to 768 MiB; accepted range is 64–4096 MiB.
  This is a decode admission budget, not a bound on total process/container memory.
- B versions image metadata, guards stale observations and commits, validates
  dimensions/orientation, and supplies bounded, cancellable backfill work.
- All corresponding native tests from these candidate patches are included.
- mument's branding, default icons, source notices, and build configuration without
  upstream analytics publishing are retained. Frontend application code remains
  at the mument baseline; the original candidate's HTML-only marker is not imported.
- The generated configuration example documents the added preview memory budget.
- No cache ledger, move-time cache reuse, background pre-generation, garbage
  collection, or other 007-C–F feature is introduced by this migration.

## Migration checks

The 56 backend paths in the source delta comprise 35 added files, 20 modified
files, and one removed legacy memory tracker. Candidate payloads were compared
with B RC3, allowing only mument notices and branding differences. No candidate
runtime, database, image cache, or configuration was copied into this repository.

Validation uses Go 1.27.3 on Linux/amd64 with cached dependencies, the default
build without `mupdf` or `analytics` tags, and a compile-only embedded placeholder.
The placeholder is removed after native tests. This is not a frontend build or
proof of browser, Docker, MuPDF, or mobile behavior.

Commands executed in `backend`:

```sh
go test -json -p 1 -count=1 ./...
go test -json -p 1 -race -count=1 ./internal/preview ./internal/adapters/fs/diskcache ./internal/database/sql ./internal/imagemeta ./pkg/indexing ./internal/web
```

Results (2026-11-17):

- Source comparison: all 55 retained backend payloads match B RC3 after removing
  migration notices and restoring the explicitly retained mument brand strings;
  comparison uses canonical Go formatting. The removed tracker is absent.
- Full native suite: exit 1, with 27 passing packages. The only failed package was
  `internal/events`: `TestConcurrentSSEClientLifecycleRace` encountered a
  `send on closed channel` panic in `handleSourceUpdates`.
- A/B race run: exit 0; all six listed packages passed (206.41 seconds).
- `internal/events` implementation and tests are unchanged from mument `main`.
  Baseline-only reruns passed: native `-count=20` and race `-count=50`. The original
  intermittent panic was not reproduced by those reruns; its trigger remains
  unresolved. This migration does not claim the full suite is green or fix that
  event lifecycle problem.
- Optional real-RAW fixtures and the opt-in batch-move measurement were skipped by
  the existing tests. Synthetic/native migration coverage is not real-image,
  browser, mobile, container, or user acceptance.
- Whitespace and source privacy-pattern checks passed. Original module/dependency
  files, icons, and release configuration without analytics are unchanged.

Detailed local logs are retained outside the public source repository. The test
placeholder was removed after validation. No container or runtime configuration
was changed.

## Next development step

007-C can be developed from this branch after migration checks. A/B retain their
candidate status: migration and automated tests are not user acceptance or
permission to merge into `main` or deploy. Existing long directory response waits
and selection errors remain separate issues, not claimed fixed here.
