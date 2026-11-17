<!-- Modified for mument, 2026-11-17: project links, prerequisites, and build instructions. -->
# Contributing to mument

This guide covers development of mument, an independently maintained derivative of FileBrowser Quantum. See [UPSTREAM.md](UPSTREAM.md) for source provenance.

## Prerequisites

- **Go 1.27.0 or newer** (see `backend/go.mod`)
- **Node.js** matching `frontend/package.json` (`^22.12.0 || ^24.0.0 || >=26.0.0`), with npm 9.0.0 or newer. `frontend/.nvmrc` selects the Node 22 series.
- **Docker** (optional, for containerized development)
- **Git** especially important on Windows -- needed for bash support.

### Optional Tools

- **ffmpeg/ffprobe**: For media features (subtitles, thumbnails, heic)
  - Ubuntu/Debian: `sudo apt-get install ffmpeg`
  - macOS: `brew install ffmpeg`
  - Windows: Download from [ffmpeg.org](https://ffmpeg.org/download.html)

- **mupdf-tools**: For PDF preview generation
  - Ubuntu/Debian: `sudo apt-get install mupdf-tools`
  - macOS: `brew install mupdf-tools`
  - Windows: Download from [mupdf.com](https://mupdf.com/downloads/)

## Quick Start

```bash
# 1. Clone the repository
git clone https://github.com/mumint-qwq/mument.git
cd mument

# 2. Initial setup - installs dependencies and creates test config
make setup

# 3. Run in development mode with hot-reloading
make dev
```

`make setup` installs all dependencies and creates a test configuration file.
`make run` starts the development server. Note: changes require ctrl+c and re-run.

## Project Architecture

### Backend (Go)
- **Entry Point**: `backend/main.go` → `backend/cmd/`
- **HTTP Server**: `backend/internal/web/` - API routes, middleware, auth
- **Storage**: SQLite via `backend/internal/database/`
- **Authentication**: Multiple providers in `backend/internal/auth/`
- **Indexing**: Real-time search in `backend/pkg/indexing/`
- **Previews**: Image/video/document generation in `backend/internal/preview/`
- **Activity**: Query, recording, and export logic in `backend/internal/activity/`

### Frontend (Vue.js + TypeScript)
- **Framework**: Vue 3 + Vite + TypeScript
- **State**: Custom store in `frontend/src/store/`
- **API Client**: Axios-based in `frontend/src/api/`
- **i18n**: 25+ languages with English as main
- **Components**: Feature-based organization

## Development Commands

### Essential Commands
```bash
make dev          # Start development server with hot-reloading
make test         # Run all tests
make lint         # Check code quality
make check-all    # Lint + tests

make build-frontend  # Build frontend only
make build-backend   # Build backend only
make build          # Build frontend and backend
make build-docker   # Build the full Docker image locally
```

### Frontend Development

Note: consider using make commands above instead.
```bash
cd frontend
npm run lint:fix  # Auto-fix linting issues
npm run i18n:sync # Sync translations changes
```

## Testing

### Running Tests
```bash
make test              # All tests
make test-backend      # Go tests with race detection
make test-frontend     # Frontend unit tests
make test-playwright   # E2E tests in Docker
```

E2E tests run with three authentication modes: standard auth, no auth, and proxy auth.

## Code Standards

### Backend (Go)
- **Linting**: `backend/.golangci.yml` with 30+ checks
- **Format**: Use `gofmt` (automated in CI)
- **Testing**: Maintain 80%+ coverage
- **Errors**: Handle all errors explicitly

### Frontend (Vue.js)
- **Linting**: ESLint with Vue 3 + TypeScript rules
- **i18n**: English is master locale, all text must use `$t('key')`
- **Types**: Use TypeScript everywhere
- **Fix**: Run `npm run lint:fix` before committing

## Build & Deployment

### Single Binary Build
The project builds into a single binary with embedded frontend:

```bash
make build-frontend  # Build Vue.js app
make build-backend   # Build Go binary with embedded assets
```

### Docker
```bash
make build-docker       # ghcr.io/mumint-qwq/mument:testing (ffmpeg and MuPDF)
make build-docker-slim  # ghcr.io/mumint-qwq/mument:testing-slim
# Optional: choose your own image repository
make build-docker IMAGE_REPO=localhost/mument
```

These commands build local images; they do not push to a registry. The internal
executable remains named `filebrowser` for compatibility. The GoReleaser project
name is `mument`, which controls release artifact names.

The upstream publishing workflows are disabled during publication preparation.
Build instructions and the retained CI workflows still need validation from a
clean checkout before the first release.

### Configuration
- **Test Config**: `backend/test_config.yaml` Auto-generated after running `make setup`

## Contributing

### Pull Request Process

1. Fork and create a feature branch
2. Make your changes following the code standards
3. Run `make dev` to build and run with your changes. Supports hot-reloading frontend and backend changes.
4. When ready, run `make check-all` to verify tests and linting
5. Submit PR with a clear description

### PR Requirements
- Clear description of changes
- All tests must pass
- Follow existing code patterns
- Update documentation if needed

### Commit Format
```
type(scope): description

Types: feat, fix, docs, refactor, test, chore
```
