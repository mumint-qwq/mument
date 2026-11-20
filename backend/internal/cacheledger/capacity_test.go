package cacheledger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapacityCandidateIndexedPagination(t *testing.T) {
	repo := testRepository(t)
	for _, tc := range []struct{ name, query, index string }{
		{"never-used", capacityUnusedPage, "capacity_unused"},
		{"old-use", capacityUsedPage, "capacity_used"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := repo.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+tc.query, 0, 0, 10, OrphanPageSize)
			require.NoError(t, err)
			defer rows.Close()
			var searched bool
			for rows.Next() {
				var id, parent, aux int
				var detail string
				require.NoError(t, rows.Scan(&id, &parent, &aux, &detail))
				t.Log(detail)
				require.NotContains(t, detail, "TEMP B-TREE")
				require.Contains(t, detail, tc.index)
				require.Contains(t, detail, "SEARCH")
				searched = true
			}
			require.NoError(t, rows.Err())
			require.True(t, searched)
		})
	}
}
