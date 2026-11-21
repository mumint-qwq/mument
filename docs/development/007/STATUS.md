# 007 cache lifecycle: implementation and validation status

The core 007-A through F implementation is integrated in local main. The final
reviewed F source is `fba5ba2c6923bc50a868a4721b4f226579c3b627`, integrated by
merge `2e6478c5cd9f39876fccab0e2126d78ee8172253` on 2026-11-21. The merge tree
is exactly the reviewed candidate tree. Subsequent closeout documentation does
not change backend or frontend product code. This is a development milestone,
not an assertion that every F01–F20 combination or production workload is tested.

## Implemented stages

| Stage | Implementation |
| --- | --- |
| A | Shared preview decode budget, bounded work and waiters, same-key coalescing, source validation and diagnostics |
| B | Versioned dimensions, orientation and aspect ratio; bounded header work outside directory GET |
| C | Durable assets/references/results and controlled same-filesystem move reuse |
| D | Scoped idle xlarge generation, foreground priority, resource deferral and restart recovery |
| E1 | Bounded resumable read-only reclamation trials |
| E2 | Evidence-based orphan confirmation, grace period, quarantine and recoverable execution |
| E3 | Space/inode admission, optional cold-result eviction, cooldown and recovery |
| F | Bounded full-content proofs and verified reuse after external or cross-mount moves |

Background generation, content reuse and physical reclamation remain explicit
configuration choices. Feature presence in main does not enable real libraries
or change an operator's storage policy.

## Final candidate evidence

The maintainer retained checksummed local artifacts for the following checks.
Earlier reports remain historical evidence with their original scope.

- Corrected F passed full backend tests in 31 packages, three core package race
  suites, writer contention, full hint queues and private-mount EXDEV checks.
- Three-round 1,000-file controlled move checks retained preview bytes with zero
  additional decoding after verification. The cross-mount core fixture contains
  independent files with the same synthetic JPEG; it does not establish paced
  throughput for 1,000 different contents.
- Seven isolated Docker containers verified core paths and E3 → F → E3 → F
  compatibility. Limits were 2 CPU, 1 GiB memory and 1 GiB swap; recorded exits
  succeeded and OOM counters were zero. Four recreated compatibility containers
  had distinct unique mount IDs.
- A full frontend and `mupdf,musl` runtime passed authentication, settings writes,
  267-file moves, ratio transfer, actual PDF rendering and diagnostic checks.
- Eight JPEG/PNG images retained all 24 small/large/xlarge publications through
  external matching and the mobile move workflow. Reuse, the mobile interval and
  verification reads added zero decodes. The small-fixture peak was 116.1 MiB,
  not a production memory bound.

The final candidate's independent 30-minute mixed run completed 410 cycles,
411 copies, 137 renames, 69 confirmed-orphan operations, 35 injected pressure
rounds and 23 private-mount recovery rounds. It made 6,021 foreground calls,
performed 405 F reuses and 136 D generations, and reclaimed 68 confirmed
objects with one bounded candidate remaining. Task failures, store failures and
panics were zero. Observed peak goroutines were 31 versus 30 initially; sampled
peak Go heap was 17 MiB. Pressure and eligibility aging were synthetic, not
real cgroup pressure or elapsed production grace.

## Large-directory measurements

Real loopback HTTP listings and eight 500-entry pages returned 4,000 distinct
paths without omissions or duplicates. Attribution windows paused F: listing
did not decode images or increase full-original read counters. Resuming the
enabled worker subsequently increased its proof-read counter.

In a desktop Chrome touch viewport, in-app SPA navigation into **mosaic** showed
the first image in 461 ms with F disabled and 478 ms enabled. Remaining card
creation completed over approximately 5.0–5.3 seconds. After jumping to the end,
the target image appeared in 133/86 ms; long tasks peaked at 99/96 ms.

These are local synthetic measurements: eight original contents are hardlinked
across 4,000 names, OS caches are warm, and a separate mixed test was running.
One browser run per setting does not establish a stable production threshold.
Earlier three-second results included full-document startup, the default view
and a fresh-database notice; they measure a different operation.

## Retained limits and remaining validation

- F initially handles scoped static JPEG/PNG originals up to 64 MiB with a
  single paced worker. Missing prior evidence falls back to demand work.
- External disappearance, independent WebDAV mutations, unregistered historical
  objects and descendants beyond E2's evidence limits remain protected without
  strong deletion evidence. Absence never authorizes unlink.
- Historical assets are not globally merged. Useful variants may remain split
  across assets. Multi-reference and unknown objects retain E's protections.
- External-tool scan-order combinations, cross-mount directory combinations,
  nested-mount F combinations and copy-midstream short writes are not all
  covered end to end.
- Full sharing-over-socket, cancellation and F/cooldown/quarantine/historical
  result combinations remain additional validation work. Corresponding core
  middleware and protocol assertions passed.
- Distinct-content/multi-variant thousand-file paced-worker performance and
  representative disk I/O latency remain measurement work. Core protocol timing
  does not substitute for these results.
- Real-library rollout and prolonged observation are separate from integration.
  Physical reclamation should follow real-library trials and protected-sample
  review, with a distinct enablement decision.

See [F walkthrough](../007f/WALKTHROUGH.md), [configuration](../007f/OPERATIONS.md)
and the [historical validation record](../007f/VALIDATION.md).
