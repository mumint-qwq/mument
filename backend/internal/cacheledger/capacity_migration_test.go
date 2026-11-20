package cacheledger

import (
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestCapacityIntentMigrationRetainsRecoveryAndCursor(t *testing.T) {
	for _, empty := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "ledger.db")
		r, err := Open(path)
		require.NoError(t, err)
		_, err = r.db.Exec("DROP TABLE capacity_intents; CREATE TABLE capacity_intents " + orphanIntentTable + `
 INSERT INTO capacity_intents VALUES(12,'legacy','key','epoch','expected','isolated stamp',1,512,'isolated');
 INSERT INTO capacity_intents VALUES(43,'settled','other','epoch2','expected','',1,512,'removed');
 DELETE FROM capacity_intents WHERE scan_id=43;`)
		require.NoError(t, err)
		if empty {
			_, err = r.db.Exec("DELETE FROM capacity_intents")
			require.NoError(t, err)
		}
		require.NoError(t, r.Close())
		r, err = Open(path)
		require.NoError(t, err)
		if !empty {
			intent, err := r.CapacityIntent(t.Context(), "key", "epoch")
			require.NoError(t, err)
			require.Equal(t, "legacy", intent.ID)
			require.Equal(t, int64(12), intent.Row)
			require.Equal(t, "isolated stamp", intent.Isolated)
			require.NoError(t, r.CapacityPhase(t.Context(), intent.ID, "restoring", intent.Isolated))
		}
		_, err = r.ReserveCapacity(t.Context(), OrphanIntent{Key: "new", Publications: "new epoch", Expected: "new stamp", Logical: 8})
		require.NoError(t, err)
		page, err := r.CapacityIntents(t.Context(), 43)
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.NoError(t, r.Close())
		// Reopening the migrated schema is idempotent.
		r, err = Open(path)
		require.NoError(t, err)
		require.NoError(t, r.Close())
	}
}
