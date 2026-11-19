// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package web

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/stretchr/testify/require"
)

func TestErrToStatusPreviewAdmissionErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"over-budget", fmt.Errorf("wrap: %w", preview.ErrPreviewOverBudget), http.StatusUnprocessableEntity},
		{"dimensions-too-large", fmt.Errorf("wrap: %w", preview.ErrPreviewDimensionsTooLarge), http.StatusUnprocessableEntity},
		{"invalid-header", fmt.Errorf("wrap: %w", preview.ErrPreviewHeaderInvalid), http.StatusUnsupportedMediaType},
		{"source-changed", fmt.Errorf("wrap: %w", preview.ErrPreviewSourceChanged), http.StatusConflict},
		{"saturated", fmt.Errorf("wrap: %w", preview.ErrPreviewSaturated), http.StatusServiceUnavailable},
		{"shutting-down", fmt.Errorf("wrap: %w", preview.ErrPreviewShuttingDown), http.StatusServiceUnavailable},
		{"unavailable", fmt.Errorf("wrap: %w", preview.ErrPreviewUnavailable), http.StatusServiceUnavailable},
		{"plain", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ErrToStatus(tc.err))
		})
	}
}
