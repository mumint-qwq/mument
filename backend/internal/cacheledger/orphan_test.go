package cacheledger

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrphanIntentMigrationKeepsOldIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	repo, err := Open(path)
	require.NoError(t, err)
	_, err = repo.db.Exec(`DROP TABLE orphan_intents;
 CREATE TABLE orphan_intents (
 scan_id INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, storage_key TEXT NOT NULL UNIQUE, publications TEXT NOT NULL,
 expected TEXT NOT NULL, isolated TEXT NOT NULL DEFAULT '', logical INTEGER NOT NULL, blocks INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('reserved','isolated','removed','held')));
 INSERT INTO orphan_intents VALUES(12,'legacy','key','epoch1','old stamp','',1,512,'reserved');
 INSERT INTO orphan_intents VALUES(13,'retained','other','epoch2','other stamp','isolated stamp',2,512,'isolated');
 INSERT INTO orphan_intents VALUES(43,'settled','other2','epoch3','stamp','',1,512,'removed');
 DELETE FROM orphan_intents WHERE scan_id=43;`)
	require.NoError(t, err)
	require.NoError(t, repo.Close())

	repo, err = Open(path)
	require.NoError(t, err)
	defer repo.Close()
	ctx := t.Context()
	old, err := repo.OrphanIntent(ctx, "key", "epoch1")
	require.NoError(t, err)
	require.Equal(t, OrphanIntent{
		ID: "legacy", Key: "key", Publications: "epoch1", Expected: "old stamp",
		Row: 12, Logical: 1, Blocks: 512, State: "moving",
	}, old)
	isolated, err := repo.OrphanIntent(ctx, "other", "epoch2")
	require.NoError(t, err)
	require.Equal(t, "retained", isolated.ID)
	require.Equal(t, "isolated", isolated.State)
	require.Equal(t, "isolated stamp", isolated.Isolated)
	newIntent := OrphanIntent{Key: old.Key, Publications: "new epoch", Expected: "new stamp", Logical: 8}
	created, err := repo.ReserveOrphan(ctx, newIntent)
	require.NoError(t, err)
	_, err = repo.ReserveOrphan(ctx, newIntent)
	require.Error(t, err)
	page, err := repo.OrphanIntents(ctx, 43)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, created.ID, page[0].ID)
	require.Equal(t, "reserved", page[0].State)
}
