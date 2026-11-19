// Modified for mument, 2026-11-17: update default branding and release links.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "net/http/pprof"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/analytics"
	"github.com/gtsteffaniak/filebrowser/backend/internal/app"
	"github.com/gtsteffaniak/filebrowser/backend/internal/icons"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/state"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/version"
	"github.com/gtsteffaniak/filebrowser/backend/internal/web"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/filebrowser/backend/swagger/docs"
	"github.com/gtsteffaniak/go-logger/logger"
	"github.com/swaggo/swag"
)

var runtimeApp *app.App

func initializeDatabase(configFile string, cliMode bool) bool {
	if cliMode {
		settings.InitializeCLI(configFile)
	} else {
		settings.Initialize(configFile)
	}

	if err := validateDatabasePaths(); err != nil {
		logger.Fatalf("%v", err)
	}

	if checkMigrationNeeded() {
		logger.Info("Old database detected, starting migration...")
		err := migrateFromBoltToSQLite()
		if err != nil {
			logger.Fatalf("Migration failed: %v", err)
		}
	}

	store, existingDb, err := state.Open(settings.Config.Server.DatabaseV2.Path)
	if err != nil {
		logger.Fatalf("could not initialize state: %v", err)
	}
	runtimeApp, err = app.WireServices(store)
	if err != nil {
		logger.Fatalf("could not wire services: %v", err)
	}
	return existingDb
}

func StartFilebrowser() {
	keepGoing, dbExists := runCLI()
	if !keepGoing {
		return
	}
	database := fmt.Sprintf("Using existing database  : %v", settings.Config.Server.DatabaseV2.Path)
	if !dbExists {
		database = fmt.Sprintf("Creating new database    : %v", settings.Config.Server.DatabaseV2.Path)
	}
	if !settings.Config.Server.DisableUpdateCheck {
		info, _ := utils.CheckForUpdates()
		if info.LatestVersion != "" {
			logger.Infof("A new version is available: %s (current: %s)", info.LatestVersion, info.CurrentVersion)
			logger.Infof("Release notes: %s", info.ReleaseNotes)
		}
		go utils.StartCheckForUpdates()
	}

	// Create context and channels for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan struct{})                          // Signals server has stopped
	shutdownComplete := make(chan web.ShutdownResult, 1) // Reports whether HTTP handlers actually drained

	// Dev mode enables development features like template hot-reloading
	_, err := os.Stat("internal/web/dist")
	// In dev mode, always use filesystem assets. Otherwise, check if internal/web/dist exists
	if !settings.Env.IsDevMode {
		settings.Env.EmbeddedFs = os.IsNotExist(err)
	}

	err = utils.SetInvalidPasswordHash()
	if err != nil {
		logger.Fatalf("Failed to set security hash: %v", err)
	}

	sourceList := []string{}
	for path, source := range settings.Config.Server.SourceMap {
		sourceList = append(sourceList, fmt.Sprintf("%v: %v", source.Name, path))
	}
	logger.Infof("Initializing mument (%v)", version.Version)
	logger.Infof("Using Config file        : %v", configPath)
	logger.Infof("Auth Methods             : %v", settings.Config.Auth.AuthMethods)
	logger.Info(database)
	logger.Infof("Sources                  : %v", sourceList)
	logger.Debugf("Using Embedded FS        : %v", settings.Env.EmbeddedFs)
	walModeStr := "OFF"
	if settings.Config.Server.IndexSqlConfig.WalMode {
		walModeStr = "WAL"
	}
	logger.Infof("SQL Journal Mode         : %v", walModeStr)
	if settings.Config.Server.CacheDirCleanup {
		logger.Debugf("clearing cache dir: %s", settings.Config.Server.CacheDir)
		fileutils.ClearCacheDir(settings.Config.Server.CacheDir)
	}
	serverConfig := settings.Config.Server
	swagInfo := docs.SwaggerInfo
	swagInfo.BasePath = settings.Config.Http.BaseURL
	swag.Register(docs.SwaggerInfo.InstanceName(), swagInfo)
	// initialize indexing and schedule indexing ever n minutes (default 5)
	if len(settings.Config.Server.SourceMap) == 0 {
		logger.Fatal("No sources configured, exiting...")
	}

	// Initialize shared index database before starting HTTP service
	isNewDb, err := indexing.InitializeIndexDB()
	if err != nil {
		logger.Fatalf("Failed to initialize index database: %v", err)
	}

	// Index metadata persistence is wired from app.WireServices during initializeDatabase.
	if isNewDb {
		if err := state.ResetAllIndexComplexities(); err != nil {
			logger.Errorf("Failed to reset index complexities: %v", err)
		}
	}

	for _, source := range settings.Config.Server.SourceMap {
		go indexing.Initialize(source, false, isNewDb)
	}
	analytics.StartReporter()
	validateUserInfo(!dbExists)
	validateOfficeIntegration()
	validateAccessRules()
	validateShareInfo()
	// Start the rootCMD in a goroutine
	go func() {
		if err := rootCMD(ctx, &serverConfig, runtimeApp, shutdownComplete); err != nil {
			logger.Fatalf("Error starting filebrowser: %v", err)
		}
		close(done) // Signal that the server has stopped
	}()
	// Wait for a shutdown signal or the server to stop
	select {
	case <-signalChan:
		logger.Info("Received shutdown signal. Shutting down gracefully...")
		cancel() // Trigger context cancellation
	case <-done:
		logger.Info("Server stopped unexpectedly. Shutting down...")
	}

	httpResult := <-shutdownComplete
	if !httpResult.Drained {
		logger.Errorf("HTTP shutdown did not drain cleanly: %d handler(s) still active (error: %v)",
			httpResult.ActiveHandlers, httpResult.HTTPError)
	}

	var stopErr error
	if httpResult.Drained {
		// Drain index maintenance and stop all indexing scanners before closing
		// the database. Maintenance is bounded so shutdown cannot hang forever.
		maintenanceCtx, cancelMaintenance := context.WithTimeout(context.Background(), 5*time.Second)
		stopErr = files.StopIndexMaintenance(maintenanceCtx)
		cancelMaintenance()
		if stopErr != nil {
			logger.Errorf("index maintenance did not stop cleanly: %v", stopErr)
		}
		indexing.StopAllScanners()

		// Give scanners a moment to finish their current scan operations
		time.Sleep(100 * time.Millisecond)
	} else {
		// Active handlers may still write metadata or submit maintenance work.
		// Do not close the queue, scanners, state, database or caches under
		// them; the process exit is the bounded termination strategy. Their
		// deferred submissions will be rejected visibly by the queue only if
		// it was already stopped, which is why it is left running here.
		logger.Errorf("skipping index maintenance drain and scanner stop because HTTP handlers are still active")
	}

	// Close the index database only when no handler or maintenance worker can
	// still use it. A timed-out maintenance drain or an HTTP shutdown with
	// active handlers must leave it open.
	busy := errors.Is(stopErr, files.ErrIndexMaintenanceBusy)
	canCloseDependencies := httpResult.Drained && !busy
	indexDB := indexing.GetIndexDB()
	if indexDB != nil {
		if canCloseDependencies {
			indexDB.Close()
		} else {
			logger.Errorf("leaving the index database open (handlers_drained=%v maintenance_busy=%v)",
				httpResult.Drained, busy)
		}
	}
	// Preview tasks may still hold decode memory after HTTP handlers drained.
	// Cancel queued work and wait for active generation to leave the budget
	// before the cache directory can be cleared on shutdown.
	previewStopped := false
	if httpResult.Drained {
		previewCtx, cancelPreview := context.WithTimeout(context.Background(), 5*time.Second)
		if err := preview.Shutdown(previewCtx); err != nil {
			logger.Warningf("preview shutdown did not complete: %v", err)
		} else {
			previewStopped = true
		}
		cancelPreview()
	}
	if settings.Config.Server.CacheDirCleanup {
		if canCloseDependencies && previewStopped {
			logger.Debugf("clearing cache dir: %s", settings.Config.Server.CacheDir)
			fileutils.ClearCacheDir(settings.Config.Server.CacheDir)
		} else {
			logger.Warningf("skipping cache cleanup (handlers_drained=%v maintenance_busy=%v preview_stopped=%v)",
				httpResult.Drained, busy, previewStopped)
		}
	}
	if canCloseDependencies {
		if err := fileutils.ClearDirectoryContents(settings.DownloadCacheDir()); err != nil {
			logger.Warningf("failed to clear download spool on shutdown: %v", err)
		}
		logger.Info("Shutdown complete.")
	} else {
		logger.Warningf("Shutdown finished with state, index database or caches left open")
	}
}

func rootCMD(ctx context.Context, serverConfig *settings.Server, a *app.App, shutdownComplete chan web.ShutdownResult) error {
	if serverConfig.NumImageProcessors < 1 {
		logger.Fatal("Image resize workers count could not be < 1")
	}
	cacheDir := settings.Config.Server.CacheDir
	numWorkers := settings.Config.Server.NumImageProcessors

	// Initialize asset filesystem before starting services
	if settings.Env.EmbeddedFs {
		embeddedAssets := web.GetEmbeddedAssets()
		subAssets, err := fs.Sub(embeddedAssets, "embed")
		if err != nil {
			logger.Fatalf("Failed to create sub filesystem: %v", err)
		}
		fileutils.InitAssetFS(subAssets, true)
	} else {
		fileutils.InitAssetFS(nil, false)
	}

	// Start preview service with the shared memory budget.
	err := preview.StartPreviewGeneratorWithBudget(numWorkers, settings.Config.Server.PreviewMemoryBudgetMB, cacheDir)
	if err != nil {
		logger.Fatalf("Error starting preview service: %v", err)
	}
	logger.Debugf("MuPDF Enabled            : %v", settings.Env.MuPdfAvailable)

	// Generate PWA icons after preview service is initialized
	if err := icons.GeneratePWAIcons(); err != nil {
		logger.Warningf("Failed to generate PWA icons: %v", err)
	}

	// Initialize PWA manifest after icons are generated
	icons.InitializePWAManifest()

	web.StartHttp(ctx, web.Deps{
		Store: a.Store,
		Files: a.Files,
		Auth:  a.Auth,
	}, shutdownComplete)
	return nil
}
