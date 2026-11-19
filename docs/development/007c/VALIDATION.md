# 007-C validation

Executed on 2026-11-18, Asia/Shanghai, on Linux amd64 with Go 1.27.4 and
Node 24.22.1. Product baseline `a10edcbdac0b84c8f6c98f5e012709f2ad1d0dad`.
Product candidate `fe73d9f04afd06f86d65ef1455f0644572151158`. The later
documentation and test changes do not change the candidate's runtime code.
The native build information records the candidate SHA and
`vcs.modified=false`. Test sources, SQL rows and temporary servers used
synthetic inputs. Raw outputs below are local and ignored.

| Phase | Command or harness | Exit and result | Evidence |
| --- | --- | --- | --- |
| Baseline relevant packages | `go -C backend test -json -p 2 -count=1 ./internal/preview ./internal/adapters/fs/diskcache ./internal/adapters/fs/fileutils ./internal/database/sql ./internal/imagemeta ./pkg/indexing` | 0, six packages passed | `.validation/007c-baseline/packages.{json,exit}` |
| Baseline full | `go test -json -p 2 -count=1 ./...` in isolated handoff archive, whose product code equals A/B | 0, 28 tested packages passed in this run | `.validation/007c-baseline/full.{json,exit}` |
| Candidate full | `go -C backend test -json -p 2 -count=1 ./...` | 0, 29 tested packages passed | `.validation/007c-final/full.{json,exit}` |
| Candidate race | `go -C backend test -json -race -p 2 -count=1 ./internal/cacheledger ./internal/preview ./internal/adapters/fs/diskcache ./internal/adapters/fs/files ./internal/adapters/fs/fileutils ./internal/database/sql ./internal/imagemeta ./pkg/indexing ./internal/web` | 0, nine packages passed | `.validation/007c-final/race.{json,exit}` |
| Later test additions | `go -C backend test -v -race -p 2 -count=1 -run '^TestLedger\|^TestProcessIntentRecovery\|^TestPublicationAndMoveCAS' ./internal/cacheledger ./internal/preview ./internal/web` | 0, process recovery, late publication, corrupt result, shutdown, move and permissions checks passed | `.validation/007c-final/targeted-race.{log,exit}` |
| Locked front build | `npm ci`, then `npm --prefix frontend run build` | 0, Vite output embedded in the backend | `.validation/007c-baseline/frontend-build.{log,exit}` |
| Front regressions | Vitest tests for folder display preferences, device settings, listing viewport, move results, selection refresh and MoveCopy | 0, six files and 87 tests passed | `.validation/007c-final/frontend-tests.{log,exit}` |
| Native build and HTTP | `go -C backend build -trimpath` with `CommitSHA` and `Version` flags, then `smoke-007c.py` | 0, health, embedded page and PNG cache reuse | `.validation/007c-final/native-{build,smoke}.{log,exit}` |
| Native rollback/re-entry | `smoke-007c.py filebrowser output --baseline filebrowser-ab --expected-commit <candidate SHA>` | 0, A/B → C → A/B → C, marked HTML and compiled JS served, PNG result and generated time retained | `.validation/007c-final/rollback-smoke.{log,exit}`, `native-*-server.log` |

The historical A/B migration encountered an intermittent SSE closed-channel
panic. The A/B and C runs above did not reproduce it. The SSE code stayed at
the baseline. The optional RAW fixtures remained skipped, and native checks
used neither MuPDF nor analytics build tags.

## C01–C18

| ID | Evidence and limits |
| --- | --- |
| C01 | Real generation and repeat hit over HTTP. Preview package test rejects same-length corrupt cached bytes, regenerates, and checks the original-byte shortcut does not publish ready. |
| C02 | Real rename/overwrite and same-variant reread retain bytes and B dimensions, orientation and new version. |
| C03 | Three candidate rounds at 1000 distinct originals warmed and then verified 1000 hits, 0 subsequent decodes and exactly two merged directory refreshes. Each changed-original control redecoded once. |
| C04 | Real 256px and 640px outputs with rotated and unrotated JPEGs. The repository also rejects an unregistered algorithm revision. |
| C05 | No adoption of an unproved legacy key. A test registers independently proven bytes under an old key and then corrupts them during a legacy write. The resulting digest mismatch misses. The target replaced with preserved mtime redecodes. |
| C06 | Two different warm JPEGs with different display ratios. After overwrite the destination returns the moved bytes and ratio in normal and paged index reads. |
| C07 | Existing HTTP partial/all failure cases passed. Batch prepare rollback and stale completion tested separately in the repository. |
| C08 | Real nested directory rename chain and recreation, adjacent `/ab` unchanged, ready descendants available after the maintenance queue drains. |
| C09 | Separate test process exits without cleanup before intent, after intent, after real rename and after commit. Restart reconciles SQLite WAL and verifies no filesystem rollback. Also replaces the moved file before recovery. These fault windows use the repository and real temporary files. |
| C10 | Admitted generation paused through a real rename refuses to publish against the old pathname. B late source and replaced target callbacks affect zero rows. Existing replacement/delete, scan epoch and queue regressions passed. |
| C11 | Strong version comparison tests include identical inode, size and mtime with changed ctime, real atomic replacement and simulated weak observations. |
| C12 | Real SQLite write lock over the HTTP move, plus repository read-only injection. Move succeeds physically, target misses and regenerates, next read hits. |
| C13 | Actual A/B and C binaries use one temporary cache and index across four launches. The old bytes and C row survive. Unit tests separately check a corrupted old-key write and restart. |
| C14 | Real cross-device move under two temp filesystems and forced same-device copy fallback over HTTP. The target cannot inherit the asset. The retained-source fallback still hits the old reference. |
| C15 | Ordinary source read permission denial with a warm cache, admin via native isolated mode, true public share middleware and an out-of-scope request that reveals no source path. |
| C16 | Existing 267-file real HTTP tests cover two directory refreshes and immediate listing reads. |
| C17 | Nine race packages and additional targeted race. Shutdown cancels a recovery waiter behind a held location gate. No production-service or physical power-loss test. |
| C18 | Full native A/B regressions, real B ratio transfer, and six existing frontend test files. No real browser or mobile acceptance. |

## Loopback HTTP scale

The measurement uses the same 800×600 synthetic JPEG fixture generator,
real admission/decode, filesystem, SQLite, and HTTP loopback requests.
GOMAXPROCS=2, two preview workers and 128 MiB decode budget. Each reread
compares its SHA-256 to the corresponding warm output, and every warm output
is distinct. The candidate measurement also asserts committed group counts.
The comparison is local and host load varied. Baseline rounds were run before
the final candidate rounds. Per-cell numbers are median / slowest, in ms.

| Images | Source | Preheat | PATCH | Immediate GET | Reread | Added full decodes, each round |
| ---: | --- | ---: | ---: | ---: | ---: | ---: |
| 100 | A/B | 7,664 / 9,060 | 48.35 / 70.47 | 16.77 / 22.50 | 8,351 / 8,766 | 100 |
| 100 | C | 4,101 / 4,107 | 86.71 / 105.93 | 10.55 / 10.58 | 88 / 122 | 0 |
| 1000 | A/B | 34,942 / 74,762 | 211.71 / 593.28 | 62.12 / 173.13 | 33,408 / 83,244 | 1000 |
| 1000 | C | 41,453 / 43,205 | 875.05 / 913.41 | 57.43 / 64.89 | 795 / 835 | 0 |

All rounds ran two merged index refreshes. Candidate rename syscalls totaled
0.96 / 1.03 ms for 100 and 10.40 / 11.49 ms for 1000. Candidate ledger
prepare and completion SQL totaled 31.26 / 38.71 ms and 325.69 / 340.79 ms.
The 100-image case committed two prepare and two completion transactions.
The 1000-image case committed 16 of each. Each round records one intent and
one outcome per image. A successful completion deletes the source reference,
writes the target reference and completes the intent. The higher PATCH cost
includes location observations, these durable transactions and B metadata
updates. The baseline run captured PATCH as a whole and has no independent
rename-syscall timing. Index refresh SQL and its row writes are separate from
ledger timings. These are HTTP measurements with synthetic input.
Repository and preview service tests are separate functional checks.

Raw baseline timing samples are `.validation/007c-perf/base-*-*.{log,exit}`.
Final runtime candidate samples are `.validation/007c-final/candidate-*-*.{log,exit}`.
Both sets contain three successful rounds per size. Test executable checksums
are in `007c-perf/binaries.sha256` and `007c-final/scale-binary.sha256`.
Reproduce with `bash scripts/development/verify-007c-scale.sh`.

## Candidate artifacts

The local artifact is `.validation/007c-final/filebrowser`, built with the
real frontend output. It reports the candidate source above and
`007c-rc1`. The served page's source marker was verified against that SHA.

- Candidate SHA-256
  `1b460be833cdcddc6e86718f680be0e0e5731073d67d0db274c5326904137003`
- A/B rollback binary SHA-256
  `6ccdcfa33b6c649f8eb22476a36ce62076fe69bc721d163c8132ddd40be7f406`

The Docker socket permission check failed. Container image build, container
cgroup samples, MuPDF and mobile acceptance remain outstanding. Native
results use loopback and process limits described above. The task also does
not simulate physical disk exhaustion or abruptly remove power from a host.
