# Development workflow

mument is the development repository. Work on feature/fix branches and separate
Git worktrees. Keep `main` for versions the maintainer has explicitly accepted;
automated test success or permission to continue development is not acceptance.

- Current migration branch: `feature/007-ab-migration`.
- Imported candidates: 007-A RC4 and 007-B RC3; see [migration record](007-ab-migration.md).
- 007-C has not been implemented. Its design and future implementation are a
  separate task; do not introduce it into the A/B migration.
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
