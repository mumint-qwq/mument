// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
package sql

import (
	"os"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func TestMain(m *testing.M) {
	if fileutils.PermDir == 0 {
		fileutils.SetFsPermissions(0o644, 0o755)
	}
	os.Exit(m.Run())
}

func TestNormalizeStartupIntegrityMode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", settings.IndexStartupIntegrityQuickCheck},
		{"quickCheck", settings.IndexStartupIntegrityQuickCheck},
		{"probe", settings.IndexStartupIntegrityProbe},
		{"off", settings.IndexStartupIntegrityOff},
		{"not-a-real-mode", settings.IndexStartupIntegrityQuickCheck},
	}
	for _, tt := range tests {
		if got := normalizeStartupIntegrityMode(tt.in); got != tt.want {
			t.Errorf("normalizeStartupIntegrityMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// pushTestIndexConfig sets CacheDir and index SQL options for NewIndexDB; returned func restores prior values.
func pushTestIndexConfig(t *testing.T, cacheDir string, idx settings.IndexSqlConfig) func() {
	t.Helper()
	prevDir := settings.Config.Server.CacheDir
	prevIdx := settings.Config.Server.IndexSqlConfig
	settings.Config.Server.CacheDir = cacheDir
	settings.Config.Server.IndexSqlConfig = idx
	return func() {
		settings.Config.Server.CacheDir = prevDir
		settings.Config.Server.IndexSqlConfig = prevIdx
	}
}

func testIndexSQLConfig(mode string) settings.IndexSqlConfig {
	return settings.IndexSqlConfig{
		WalMode:               false,
		BatchSize:             1000,
		CacheSizeMB:           32,
		DisableReuse:          false,
		StartupIntegrityCheck: mode,
	}
}

func TestNewIndexDB_StartupProbe_NewFile(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityProbe))
	defer pop()

	db, _, err := NewIndexDB("probe_new", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
}

func TestNewIndexDB_StartupProbe_ReopenWithData(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityProbe))
	defer pop()

	db, _, err := NewIndexDB("probe_reopen", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	info := &iteminfo.FileInfo{
		Path: "/a.txt",
		ItemInfo: iteminfo.ItemInfo{
			Name:    "a.txt",
			Size:    12,
			ModTime: time.Unix(1, 0),
			Type:    "text/plain",
			Hidden:  false,
		},
	}
	if err = db.InsertItem("src1", "/a.txt", info); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	var db2 *IndexDB
	db2, _, err = NewIndexDB("probe_reopen", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
}

func TestNewIndexDB_StartupQuickCheckStillWorks(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityQuickCheck))
	defer pop()

	db, _, err := NewIndexDB("quick_default", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
}

func TestNewIndexDB_StartupOff(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()

	db, _, err := NewIndexDB("off_mode", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
}

func TestIndexDBAddsAndPersistsDisplayAspectRatio(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()

	legacy, err := createIndexDB("aspect_legacy", "OFF", "", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE index_items (
		source TEXT NOT NULL, path TEXT NOT NULL, parent_path TEXT NOT NULL,
		name TEXT NOT NULL, size INTEGER NOT NULL, mod_time INTEGER NOT NULL,
		type TEXT NOT NULL, is_dir BOOLEAN NOT NULL, is_hidden BOOLEAN NOT NULL,
		has_preview BOOLEAN NOT NULL, last_updated INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (source, path)
	)`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	_, err = legacy.Exec(`INSERT INTO index_items
		(source, path, parent_path, name, size, mod_time, type, is_dir, is_hidden, has_preview, last_updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"src", "/preserved.jpg", "/", "preserved.jpg", 1, 1, "image/jpeg", false, false, true, 1)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, isNew, err := NewIndexDB("aspect_legacy", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !isNew {
		t.Fatal("schema backfill should request an initial full scan")
	}
	preserved, err := db.GetItem("src", "/preserved.jpg")
	if err != nil || preserved.Name != "preserved.jpg" {
		t.Fatalf("legacy row was not preserved: info=%#v err=%v", preserved, err)
	}
	// The accepted v2.0.2 backend writes an explicit legacy column list. The
	// default on the new column keeps that write path valid after a rollback.
	_, err = db.Exec(`INSERT INTO index_items
		(source, path, parent_path, name, size, mod_time, type, is_dir, is_hidden, has_preview, last_updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"src", "/legacy.jpg", "/", "legacy.jpg", 1, 1, "image/jpeg", false, false, true, 1)
	if err != nil {
		t.Fatalf("legacy insert after migration: %v", err)
	}

	info := &iteminfo.FileInfo{
		Path: "/wide.jpg",
		ItemInfo: iteminfo.ItemInfo{
			Name: "wide.jpg", Size: 123, ModTime: time.Unix(2, 0), Type: "image/jpeg",
			HasPreview: true, DisplayAspectRatio: 16.0 / 9.0,
		},
	}
	if err = db.InsertItem("src", info.Path, info); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetItem("src", info.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayAspectRatio != info.DisplayAspectRatio {
		t.Fatalf("display aspect ratio = %v, want %v", got.DisplayAspectRatio, info.DisplayAspectRatio)
	}
}
