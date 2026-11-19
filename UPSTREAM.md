# Source provenance

mument is an independently maintained derivative of
[FileBrowser Quantum](https://github.com/gtsteffaniak/filebrowser), itself derived
from [File Browser](https://github.com/filebrowser/filebrowser).

- Upstream baseline: `2c071d1e6eb48d4b88da2657885d345663a0a88b`.
- Local accepted source commit before publication cleanup: `72812feeb03b7cac51734e36ca35e05b1b8964d5`.
- Snapshot prepared: 2026-11-17 (Asia/Shanghai).
- The public repository starts with a source snapshot; earlier local Git history
  is not included.
- Publication cleanup updates README.md and excludes local deployment scripts,
  internal planning documents, and selected local test and diagnostic tooling.
- Application changes include mobile image browsing and mosaic layouts, file
  organization improvements, device-specific opening preferences, per-folder
  display preferences, and client diagnostics.

The source retains the Apache-2.0 license and applicable upstream notices.
This provenance document does not replace the license or file-level notices.
The upstream branch-management and publishing workflows are retained with a
`.disabled` suffix during publication preparation. The three build/test workflows
remain enabled; they have not yet been validated for mument.

Build, release configuration, and third-party license checks are still pending.

## Publication changes (2026-11-17)

- Local Docker build tags and the floating-tag policy default use
  `ghcr.io/mumint-qwq/mument`; full and slim builds have distinct testing tags.
- The GoReleaser project name is `mument`; the executable name, Go module path,
  imports, and version-injection paths remain unchanged for compatibility.
- The contribution guide and pull request template refer to mument. The guide
  records toolchain requirements from the source configuration and distinguishes
  native builds from Docker builds.
- Default application name, description, setup prompt, CLI branding, and release
  links identify mument. The checked-in generated configuration example reflects
  those defaults. Hardcoded upstream canonical links were removed from the HTML.
- Existing custom brand names and authentication issuer identifiers are unchanged.
  Default icon assets use the selected mint folder with a white lowercase m.
  The `analytics` build tag is omitted from Docker and GoReleaser builds, so those
  builds use the implementation without upstream analytics publishing.

## Branding validation (2026-11-17)

- CLI parsing and setup YAML tests passed.
- Settings tests passed with `TestHTTPListenAddr` excluded: the sandbox rejects
  listening sockets, so the full settings suite could not pass in this environment.
- CLI-only tests used a temporary embed placeholder, which was removed afterward;
  no frontend build or full application smoke test has been performed.

## Icon selection (2026-11-17)

- The selected imagegen concept replaces both default favicon assets.
- `favicon.png` preserves the selected image unchanged. `favicon.svg` embeds the
  same PNG to keep browser/login artwork consistent; it is not a traced vector.
- Existing startup icon generation uses the PNG for PWA and Apple icon sizes.
- PNG dimensions and SVG parsing were checked; embedded image bytes match exactly.
  Runtime rendering and generated desktop icons have not yet been checked.

## Analytics build configuration (2026-11-17)

- Removed `analytics` from the full Docker build, slim Docker build, and all seven
  GoReleaser build definitions. Existing `mupdf` and `musl` tags are retained.
- Without this tag, `publish_stub.go` reports `PublishSupported() == false` and
  its reporter functions do nothing. The settings API rejects enabling publishing.
- Local diagnostics and the normal application features are unchanged. Upstream
  analytics source remains in the repository; explicitly supplying `-tags analytics`
  in a custom build can still enable that implementation.
- Validation: analytics package tests passed. Go file selection was checked with
  no tags, `mupdf,musl`, `musl`, and `mupdf`; all select `publish_stub.go` and exclude
  `publish.go`. Docker images and release binaries have not yet been built.

## Source modification notices (2026-11-17)

[MODIFICATIONS.md](MODIFICATIONS.md) lists modified and added source files against
our upstream baseline. Modified text files carry in-file notices; modified JSON
and PNG files have adjacent `.NOTICE` files. Existing upstream declarations and
the Aves BSD-3-Clause license text are retained. These records do not replace
third-party license requirements or claim a completed dependency-license review.

## Development candidate migration (2026-11-17)

The `feature/007-ab-migration` branch imports the 007-A RC4 preview resource
protection and 007-B RC3 image metadata candidate changes. See
[the migration record](docs/development/007-ab-migration.md) for source commits,
scope, and current verification. These candidates are not merged into `main` or
user-accepted as a mument version. See the separate [007-C development record](docs/development/007c/IMPLEMENTATION.md).

Migration verification: the six affected-package race checks passed. The full
native suite encountered an intermittent panic in the unchanged events package;
baseline-only repeated checks did not reproduce it. See the migration record for
all limits, skipped optional checks, and the unresolved event lifecycle issue.
