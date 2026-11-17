// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
package indexing

import (
	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"golang.org/x/net/webdav"
)

// SetTestIndex allows tests to register mock indices without database initialization
// This is useful for testing code that depends on GetIndex() without needing full setup
func SetTestIndex(name string, path string) {
	indexesMutex.Lock()
	defer indexesMutex.Unlock()

	idx := &Index{
		Source: settings.Source{
			Name: name,
			Path: path,
		},
		WebdavLock: webdav.NewMemLS(),
		mock:       true,
	}
	indexes[name] = idx
}

// SetTestIndexWithDB registers a ready mock index backed by an existing SQLite
// database. It is for handler-level tests that need index pagination without a
// full scanner/app wiring.
func SetTestIndexWithDB(name, path string, db *dbsql.IndexDB) *Index {
	indexesMutex.Lock()
	defer indexesMutex.Unlock()

	idx := &Index{
		Source: settings.Source{
			Name: name,
			Path: path,
			Config: settings.SourceConfig{
				ResolvedRules: settings.ResolvedRulesConfig{NoRules: true},
			},
		},
		db:                  db,
		WebdavLock:          webdav.NewMemLS(),
		mock:                true,
		scanUpdatedPaths:    make(map[string]bool),
		folderSizes:         make(map[string]uint64),
		folderSizesUnsynced: make(map[string]struct{}),
	}
	idx.Status = READY
	indexes[name] = idx
	return idx
}

// ClearTestIndices removes all test indices - call in test cleanup
func ClearTestIndices() {
	indexesMutex.Lock()
	defer indexesMutex.Unlock()
	indexes = make(map[string]*Index)
}
