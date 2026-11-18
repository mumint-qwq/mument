# 007-C implementation

Branch `feature/007c-cache-move-reuse`, based on handoff `0127b62` and
product ancestor `a10edcb`. Scope and lifecycle are described in [DESIGN.md](DESIGN.md).
Evidence and remaining validation limits are in [VALIDATION.md](VALIDATION.md).

| Area | Files |
| --- | --- |
| Durable references, versions, integrity and move intents | `backend/internal/cacheledger/` |
| Preview integration and bounded physical cache reads | `backend/internal/preview/{ledger,preview,source,observe}.go`, `backend/internal/adapters/fs/diskcache/file_cache.go` |
| Authorized batch groups and legacy cache handling | `backend/internal/web/{resource,preview}.go` |
| Rename outcome, fallback and index impacts | `backend/internal/adapters/fs/files/{cache_moves,files,move_index_batch,index_refresh_queue}.go`, `backend/internal/adapters/fs/fileutils/file.go` |
| New target version and metadata guard | `backend/internal/database/sql/image_move.go`, `backend/pkg/indexing/image_move.go` |
| Regression and scale harness | `backend/internal/cacheledger/repository_test.go`, `backend/internal/preview/ledger_test.go`, `backend/internal/web/{cache_move_test,cache_move_cases_test,batch_move_share_test}.go` |
| Portable tooling | `scripts/development/{bootstrap-go.sh,verify-007c-scale.sh,smoke-007c.py}` |

The candidate build uses a genuine Vite frontend embed and the Linux amd64 Go
backend without MuPDF or analytics build tags. Suggested local review branch
`test/007c-cache-move-reuse-rc1` and suggested immutable candidate tag
`007c-cache-move-reuse-rc1` may be created after review. The executable stays
`filebrowser` for project compatibility. Container review can use the existing
Dockerfile and local mument tags with a temporary synthetic source, a loopback
port, 2 CPUs, 1 GiB memory and 2 GiB combined memory plus swap. A container
artifact requires a separate build and measurement. Runtime databases and
build products stay under ignored `.validation/`.

A/B and C are local development candidates. Update, merge, push, publication
and deployment are separate steps. A binary rollback changes the code that
runs against current data. Files already moved stay in their new locations,
and an old database snapshot must never be applied as part of the rollback.
