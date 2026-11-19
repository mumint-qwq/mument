# 007-D operations

## Enable a library

D starts disabled. Enable it in the server configuration and restart the candidate. Use the configured source `name` and a clean path relative to that source. This example names a synthetic library.

```yaml
server:
  previewWarmup:
    enabled: true
    scopes:
      - source: synthetic
        directory: Pictures
        mountPoint: "."
```

`directory: "."` explicitly authorizes the source root. With `mountPoint: "."`, the source root must itself be a mount in the server's namespace. A nested mount may be named relative to the source, provided it contains the authorized directory. Omit `mountPoint` for directories on the source's own filesystem. Use it for removable disks and bind-mounted source roots so an uncovered directory after an unmount stays offline. Files below additional mounts require a separate, nonoverlapping scope rooted at the intended mount. The host media guard continues to verify the configured disk UUID at deployment start.

Enabled configurations allow 1–16 scopes. Empty, unknown, indexing-disabled, noncanonical, duplicate and component-overlapping ranges fail startup validation. The resolved source paths are checked for lexical overlap across source names. A filesystem alias created with a separate bind mount can have its own lexical range and ledger namespace. The worker requires previews and resizing, a retained cache and a reusable index. Disabled configurations leave the worker and its schema initialization dormant.

The worker requires Linux with strong B/C file identity, `openat2`, mount-ID support in `statx` and readable cgroup v2 CPU metrics. JPEG and PNG use the existing in-process raster decode, the regular `xlarge` variant and C's ledger. Valid small originals are recorded as unnecessary. The worker excludes other extensions, APNG and external JPEG fallback. The 1 MiB header bound can exclude files with unusually large metadata before pixel data. Original preference, `small` and `large` stay on their existing request paths.

## View and control

Use the authenticated admin endpoint `GET /api/settings/preview-warmup`. Scope numbers are the one-based positions in the configuration. States and aggregate counters contain scope numbers, counts, and reason codes. They omit file and directory names. `warmupMaxHoldNanos` measures the longest background reservation, including decoding, publication and memory settlement. An already running decode can keep its permit until it returns. `decodeWaitNanos` is the shared interactive budget queue wait accumulated since startup.

Send JSON with `POST /api/settings/preview-warmup` using the same admin session.

| Operation | Request body | Effect |
| --- | --- | --- |
| Pause | `{"action":"pause"}` | Stop new claims. An admitted decode can finish. |
| Resume | `{"action":"resume"}` | Resume automatic and previously requested manual work. |
| Fill scope 1 | `{"action":"fill","scope":1}` | Queue one bounded manual pass through scope 1. It also checks completed versions, with ordinary idle, retry and path safeguards. |
| Cancel | `{"action":"cancel"}` | Cancel the current round and discard the manual pass. Automatic work can resume on the next eligible turn. |

The pause switch, manual command and cursor are process-local. The per-version completion and retry fields persist. Disable across restarts with `enabled: false` or by removing the block.

## Scheduling and pressure

A 500 ms ticker allows up to one guarded candidate attempt per turn. The worker makes one SQLite page query of at most 16 rows and rotates scopes. Every other automatic turn in a scope uses the path cursor. The other turns can use the latest version guards for that source or a directory browsed within five minutes. Recent queries read up to 16 source rows before applying the configured directory prefix. History still progresses when recent rows belong to a sibling directory or arrive continuously. The five-second file age and one-second foreground quiet window are conservative initial thresholds.

A claim requires available, writable cache space with a 512 MiB reserve and at least 32 free inodes where inode counts are available. cgroup v2 memory checks consider each visible finite limit with 96 MiB headroom and check the particular decode estimate again before admission. A CPU throttling increase since the previous sample defers work. When the process has no visible finite memory limit, native fallback requires a 512 MiB host reserve and caps a background estimate at 96 MiB. Missing CPU or usable memory metrics pause work. The main preview budget still applies. Per-candidate memory pressure defers the version for 30 seconds without consuming an error retry.

An unavailable scope delays only that scope. The worker pins its usable directory identities for the life of the process. A remount must expose the expected device and directory identities. If the scope drops offline or its index becomes unready during the final publication check, the scheduler retries the same indexed version after recovery. Mount IDs must also match between claim and the read/publication checks. Intentional changes to disk identity require checking the mount and restarting. Version mismatch retires the stale index observation. It becomes eligible when B records the new version. Temporary file/header/store errors get two retries after 15 and 60 seconds. Unsupported or over-budget versions are terminal until the version changes or an administrator runs an explicit fill. Space, foreground, offline and CPU decisions do not consume those retries.

On SIGTERM the worker stops taking work. After HTTP shutdown, the process waits up to five seconds for the warmup worker and its active flight. If a filesystem call remains blocked, the shutdown log reports the drain timeout and the process leaves the preview service, ledger, index and caches open until exit. The next boot uses the normal per-version recovery.

## Roll back

Stop the D candidate, remove `previewWarmup` from its config and switch to the accepted C binary and existing data/config mounts. Keep source files, cache files, the C preview ledger and the index in place. C ignores the three D-specific index columns and two partial indexes. Its preview paths can reuse D's valid ledger publications. Leaving the columns and indexes in place preserves progress when returning to D. The migration adds state only to the index database. If the index is rebuilt, D can recover via bounded scans and existing ledger hits. It can repeat terminal work whose index record was lost.

## Walkthrough

A new JPEG under `synthetic/Pictures` is observed by B and gets an `image_guard`. On a recent or historical tick the worker sees that guard, waits until file and foreground activity are quiet, and opens the authorized directory from its source descriptor. The candidate's current stat must match the index. The warmup entry resolves the ordinary C `xlarge` variant. A ledger hit checks metadata and the physical cache file size without reading thumbnail bytes. A miss joins a preview flight and only starts decoding when the shared A budget is idle. The decoder reads from the verified descriptor. It checks the file and live directory again before publishing through C. The worker then stores a completion marker conditional on the same index version. On restart, a crash before the marker leaves the version eligible, and a crash after publication is recovered by the ledger lookup. The first foreground delivery verifies the thumbnail contents and records `last_used_at`.
