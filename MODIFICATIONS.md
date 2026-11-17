# Source modification inventory

This inventory compares the mument publication snapshot with FileBrowser Quantum
baseline `2c071d1e6eb48d4b88da2657885d345663a0a88b`. It records changes already present in the
accepted local derivative as well as publication preparation; it does not claim
that all changes were authored on the snapshot date. See [UPSTREAM.md](UPSTREAM.md).

Existing copyright and license notices remain applicable. The inventory does not
replace them. JSON and PNG files listed below have adjacent `.NOTICE` files so
their data formats do not need to be changed.

## Modified upstream files

Changes cover mobile browsing and mosaic layout, file operations and directory
loading, device and folder preferences, diagnostics, related tests and generated
API documentation, and mument branding/build documentation.

| File | Declaration |
| --- | --- |
| `.github/PULL_REQUEST_TEMPLATE.md` | In-file comment |
| `.github/scripts/docker-floating-tag-policy.sh` | In-file comment |
| `.gitignore` | In-file comment |
| `CONTRIBUTING.md` | In-file comment |
| `README.md` | In-file comment |
| `_docker/Dockerfile` | In-file comment |
| `_docker/Dockerfile.slim` | In-file comment |
| `backend/.goreleaser.yaml` | In-file comment |
| `backend/cmd/cli.go` | In-file comment |
| `backend/cmd/kong.go` | In-file comment |
| `backend/cmd/root.go` | In-file comment |
| `backend/internal/activity/diff.go` | In-file comment |
| `backend/internal/adapters/fs/files/files.go` | In-file comment |
| `backend/internal/adapters/fs/files/service.go` | In-file comment |
| `backend/internal/database/sql/index.go` | In-file comment |
| `backend/internal/database/sql/index_startup_test.go` | In-file comment |
| `backend/internal/database/sql/tempdb.go` | In-file comment |
| `backend/internal/database/users/users.go` | In-file comment |
| `backend/internal/imagemeta/orientation.go` | In-file comment |
| `backend/internal/imagemeta/stub.go` | In-file comment |
| `backend/internal/state/users.go` | In-file comment |
| `backend/internal/web/http.go` | In-file comment |
| `backend/internal/web/httpRouter.go` | In-file comment |
| `backend/internal/web/http_preview_test.go` | In-file comment |
| `backend/internal/web/middleware.go` | In-file comment |
| `backend/internal/web/preview.go` | In-file comment |
| `backend/internal/web/resource.go` | In-file comment |
| `backend/internal/web/server.go` | In-file comment |
| `backend/internal/web/users.go` | In-file comment |
| `backend/internal/web/users_patch_test.go` | In-file comment |
| `backend/pkg/indexing/getDirInfoCore_test.go` | In-file comment |
| `backend/pkg/indexing/indexingFiles.go` | In-file comment |
| `backend/pkg/indexing/indexingScanner.go` | In-file comment |
| `backend/pkg/indexing/iteminfo/fileinfo.go` | In-file comment |
| `backend/pkg/indexing/mutate.go` | In-file comment |
| `backend/pkg/indexing/test_helpers.go` | In-file comment |
| `backend/pkg/settings/config.go` | In-file comment |
| `backend/pkg/settings/structs.go` | In-file comment |
| `backend/pkg/settings/user_enforcement_fields.go` | In-file comment |
| `backend/pkg/settings/user_profile.go` | In-file comment |
| `backend/pkg/settings/user_profile_merge.go` | In-file comment |
| `backend/pkg/settings/user_profile_projection.go` | In-file comment |
| `backend/swagger/docs/docs.go` | In-file comment |
| `backend/swagger/docs/swagger.json` | Adjacent `.NOTICE` |
| `backend/swagger/docs/swagger.yaml` | In-file comment |
| `frontend/package.json` | Adjacent `.NOTICE` |
| `frontend/public/config.generated.yaml` | In-file comment |
| `frontend/public/img/icons/favicon.png` | Adjacent `.NOTICE` |
| `frontend/public/img/icons/favicon.svg` | In-file comment |
| `frontend/public/index.html` | In-file comment |
| `frontend/src/api/media.js` | In-file comment |
| `frontend/src/api/resources.js` | In-file comment |
| `frontend/src/components/ContextMenu.vue` | In-file comment |
| `frontend/src/components/StatusBar.vue` | In-file comment |
| `frontend/src/components/files/FileList.vue` | In-file comment |
| `frontend/src/components/files/FileTree.vue` | In-file comment |
| `frontend/src/components/files/Icon.vue` | In-file comment |
| `frontend/src/components/files/ListingItem.vue` | In-file comment |
| `frontend/src/components/files/Scrollbar.vue` | In-file comment |
| `frontend/src/components/prompts/Delete.vue` | In-file comment |
| `frontend/src/components/prompts/MoveCopy.vue` | In-file comment |
| `frontend/src/components/prompts/Upload.vue` | In-file comment |
| `frontend/src/components/settings/UserProfilePreferences.vue` | In-file comment |
| `frontend/src/components/settings/ViewMode.vue` | In-file comment |
| `frontend/src/components/sidebar/General.vue` | In-file comment |
| `frontend/src/css/listing.css` | In-file comment |
| `frontend/src/i18n/en.json` | Adjacent `.NOTICE` |
| `frontend/src/i18n/zh-cn.json` | Adjacent `.NOTICE` |
| `frontend/src/i18n/zh-tw.json` | Adjacent `.NOTICE` |
| `frontend/src/main.ts` | In-file comment |
| `frontend/src/store/getters.ts` | In-file comment |
| `frontend/src/store/mutations.ts` | In-file comment |
| `frontend/src/store/state.ts` | In-file comment |
| `frontend/src/store/types.ts` | In-file comment |
| `frontend/src/utils/moment.js` | In-file comment |
| `frontend/src/utils/upload.js` | In-file comment |
| `frontend/src/utils/upload.test.js` | In-file comment |
| `frontend/src/utils/userProfileSections.js` | In-file comment |
| `frontend/src/views/Files.vue` | In-file comment |
| `frontend/src/views/bars/Default.vue` | In-file comment |
| `frontend/src/views/files/ListingView.vue` | In-file comment |
| `frontend/src/views/settings/Profile.vue` | In-file comment |
| `frontend/tests/mocks/setup.js` | In-file comment |
| `frontend/vite.config.ts` | In-file comment |
| `makefile` | In-file comment |

## Added derivative files

These files were absent from the upstream baseline. Their inclusion here is a
provenance record, not a replacement for any license notices they contain.

- `.githooks/pre-commit`
- `UPSTREAM.md`
- `backend/internal/adapters/fs/files/index_refresh_queue.go`
- `backend/internal/adapters/fs/files/index_refresh_queue_rc2_test.go`
- `backend/internal/adapters/fs/files/index_refresh_queue_rc3_test.go`
- `backend/internal/adapters/fs/files/index_refresh_queue_test.go`
- `backend/internal/adapters/fs/files/index_work_probe.go`
- `backend/internal/adapters/fs/files/metadata.go`
- `backend/internal/adapters/fs/files/metadata_test.go`
- `backend/internal/adapters/fs/files/move_index_batch.go`
- `backend/internal/database/sql/index_preview_test.go`
- `backend/internal/database/sql/listing.go`
- `backend/internal/database/sql/listing_migration_test.go`
- `backend/internal/database/sql/listing_test.go`
- `backend/internal/database/sql/metrics.go`
- `backend/internal/database/users/devices.go`
- `backend/internal/database/users/devices_test.go`
- `backend/internal/diagnostics/config.go`
- `backend/internal/diagnostics/dedupe.go`
- `backend/internal/diagnostics/limiter.go`
- `backend/internal/diagnostics/limiter_test.go`
- `backend/internal/diagnostics/schema.go`
- `backend/internal/diagnostics/schema_test.go`
- `backend/internal/diagnostics/service.go`
- `backend/internal/diagnostics/service_test.go`
- `backend/internal/diagnostics/writer.go`
- `backend/internal/diagnostics/writer_blocking_test.go`
- `backend/internal/diagnostics/writer_failure_test.go`
- `backend/internal/diagnostics/writer_test.go`
- `backend/internal/state/devices_test.go`
- `backend/internal/web/batch_move_get_guard_test.go`
- `backend/internal/web/batch_move_index_test.go`
- `backend/internal/web/batch_move_measure_test.go`
- `backend/internal/web/batch_move_share_test.go`
- `backend/internal/web/diagnostics.go`
- `backend/internal/web/diagnostics_extra_test.go`
- `backend/internal/web/diagnostics_test.go`
- `backend/internal/web/directory_preview.go`
- `backend/internal/web/listing.go`
- `backend/internal/web/listing_test.go`
- `backend/internal/web/metadata.go`
- `backend/internal/web/metadata_test.go`
- `backend/internal/web/shutdown_test.go`
- `backend/internal/web/users_devices_test.go`
- `backend/pkg/indexing/image_dimensions.go`
- `backend/pkg/indexing/image_dimensions_test.go`
- `backend/pkg/indexing/refresh_probe.go`
- `backend/pkg/indexing/refresh_test_hook.go`
- `backend/pkg/indexing/write_epoch.go`
- `backend/pkg/indexing/write_epoch_test.go`
- `backend/pkg/settings/device_settings_test.go`
- `backend/pkg/settings/media_metadata.go`
- `backend/pkg/settings/media_metadata_test.go`
- `diagnostics/fixtures/cases.json`
- `diagnostics/schema-v1.json`
- `frontend/public/licenses/aves-BSD-3-Clause.txt`
- `frontend/src/api/diagnostics.js`
- `frontend/src/api/listing.js`
- `frontend/src/api/listing.test.js`
- `frontend/src/api/media.metadataBatch.test.js`
- `frontend/src/components/files/FileList.folderOnly.test.js`
- `frontend/src/components/files/ListingItemBatch.vue`
- `frontend/src/components/prompts/MoveCopy.test.js`
- `frontend/src/components/settings/DefaultSorting.vue`
- `frontend/src/components/settings/DiagnosticsSettings.vue`
- `frontend/src/store/folderDisplayPreferences.test.js`
- `frontend/src/store/selectionRefresh.test.js`
- `frontend/src/utils/deviceIdentity.js`
- `frontend/src/utils/deviceIdentity.test.js`
- `frontend/src/utils/deviceSaveQueue.js`
- `frontend/src/utils/deviceSaveQueue.test.js`
- `frontend/src/utils/deviceSettings.js`
- `frontend/src/utils/deviceSettings.test.js`
- `frontend/src/utils/diagnostics/config.js`
- `frontend/src/utils/diagnostics/errors.js`
- `frontend/src/utils/diagnostics/errors.test.js`
- `frontend/src/utils/diagnostics/listingBridge.js`
- `frontend/src/utils/diagnostics/longTasks.js`
- `frontend/src/utils/diagnostics/longTasks.test.js`
- `frontend/src/utils/diagnostics/recorder.js`
- `frontend/src/utils/diagnostics/recorder.test.js`
- `frontend/src/utils/diagnostics/runtime.js`
- `frontend/src/utils/diagnostics/runtime.test.js`
- `frontend/src/utils/diagnostics/schema.js`
- `frontend/src/utils/diagnostics/schema.test.js`
- `frontend/src/utils/diagnostics/telemetry.js`
- `frontend/src/utils/diagnostics/transport.js`
- `frontend/src/utils/diagnostics/transport.test.js`
- `frontend/src/utils/directoryItems.js`
- `frontend/src/utils/directoryItems.test.js`
- `frontend/src/utils/folderDisplayPreferences.js`
- `frontend/src/utils/folderDisplayPreferences.test.js`
- `frontend/src/utils/interactionGuard.js`
- `frontend/src/utils/interactionGuard.test.js`
- `frontend/src/utils/listAnchor.js`
- `frontend/src/utils/listAnchor.test.js`
- `frontend/src/utils/listingFeed.js`
- `frontend/src/utils/listingFeed.test.js`
- `frontend/src/utils/listingLifecycle.js`
- `frontend/src/utils/listingLifecycle.test.js`
- `frontend/src/utils/listingMountBridge.js`
- `frontend/src/utils/listingMountBridge.test.js`
- `frontend/src/utils/listingMountPlan.js`
- `frontend/src/utils/listingMountPlan.test.js`
- `frontend/src/utils/listingRestore.js`
- `frontend/src/utils/listingRestore.test.js`
- `frontend/src/utils/listingViewport.js`
- `frontend/src/utils/listingViewport.test.js`
- `frontend/src/utils/mosaicAnchor.js`
- `frontend/src/utils/mosaicDensity.js`
- `frontend/src/utils/mosaicDensity.test.js`
- `frontend/src/utils/mosaicImageSource.js`
- `frontend/src/utils/mosaicImageSource.test.js`
- `frontend/src/utils/mosaicLayout.js`
- `frontend/src/utils/mosaicLayout.test.js`
- `frontend/src/utils/mosaicOriginalImage.js`
- `frontend/src/utils/mosaicOriginalImage.test.js`
- `frontend/src/utils/mosaicPinch.js`
- `frontend/src/utils/mosaicPinch.test.js`
- `frontend/src/utils/mosaicPreferences.js`
- `frontend/src/utils/moveResults.js`
- `frontend/src/utils/moveResults.test.js`
- `frontend/src/utils/pagedListingPref.js`
- `frontend/src/utils/pagedListingPref.test.js`
- `frontend/src/utils/scrollActivity.js`
- `frontend/src/utils/scrollActivity.test.js`
- `frontend/src/utils/scrollbarClickGuard.js`
- `frontend/src/utils/scrollbarClickGuard.test.js`
- `frontend/src/utils/selectionKeys.js`
- `frontend/src/utils/selectionKeys.test.js`
- `frontend/src/utils/thumbnailLoadPolicy.js`
- `frontend/src/utils/thumbnailLoadPolicy.test.js`
- `frontend/src/utils/thumbnailLoadScheduler.js`
- `frontend/src/utils/thumbnailLoadScheduler.test.js`
- `frontend/src/utils/thumbnailVisibility.js`
- `frontend/src/utils/thumbnailVisibility.test.js`
- `frontend/src/utils/uploadEnumeration.js`
- `frontend/src/utils/uploadEnumeration.test.js`

## Disabled upstream workflows

These files retain their upstream contents and have only been renamed to prevent
execution during source publication preparation.

- `.github/workflows/create-dev-branch.yaml.disabled`
- `.github/workflows/promote-dev-to-beta.yaml.disabled`
- `.github/workflows/tag.yaml.disabled`
- `.github/workflows/update-build-images.yaml.disabled`
