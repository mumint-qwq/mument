# Development workflow

mument is the development repository. Work on feature/fix branches and separate
Git worktrees. Keep `main` for versions the maintainer has explicitly accepted;
automated test success or permission to continue development is not acceptance.

- Current migration branch: `feature/007-ab-migration`.
- Imported candidates: 007-A RC4 and 007-B RC3; see [migration record](007-ab-migration.md).
- 007-C development is on `feature/007c-cache-move-reuse` based on the
  migrated A/B code. See the [task handoff](007c-cache-move-reuse.md),
  [implementation and build information](007c/IMPLEMENTATION.md), and
  [validation](007c/VALIDATION.md). The feature is awaiting maintainer review.
- 007-D develops idle mosaic previews on `feature/007d-idle-preview-worker` from the accepted A/B/C tree. See the [design](007d/DESIGN.md) [operations and walkthrough](007d/OPERATIONS.md), and [validation](007d/VALIDATION.md).
- 007-E1 adds a bounded read-only preview inventory on `feature/007e-cache-reclamation`. See the [design and walkthrough](007e/DESIGN.md) and [replay commands](007e/VALIDATION.md).
- 007-E2 adds confirmed orphan reclamation on feature/007e2-orphan-reclamation. See the [design and walkthrough](007e2/DESIGN.md) and [replay commands](007e2/VALIDATION.md).
- Preserve mument branding, upstream attribution, and build configuration without
  upstream analytics publishing when porting older Quantum patches.
- Development, Git merging, pushing source, and deployment are separate actions.
  Do not change running services or private runtime configuration as part of a
  source-only task.
- Keep test data synthetic or upstream-provided. Do not commit runtime databases,
  private logs, credentials, dependency directories, build outputs, or personal media.
- Record source commits, candidate identity, relevant checks and limitations.
  Only claim browser/mobile/container validation when actually performed.

The historical local repository and deployment tooling are retained separately.
Do not copy their Git metadata, private configuration, or logs into this repository.
