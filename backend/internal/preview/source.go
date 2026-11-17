// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"context"
	"fmt"
	"os"
	"time"
)

// sourceBinding ties an admission estimate to the exact file that was inspected.
// It is captured from an open file descriptor, so the logical size comes from
// the file itself rather than from index metadata (which may report allocated
// blocks instead of the byte length).
type sourceBinding struct {
	info    os.FileInfo
	size    int64
	modTime time.Time
}

func newSourceBinding(f *os.File) (sourceBinding, error) {
	info, err := f.Stat()
	if err != nil {
		return sourceBinding{}, fmt.Errorf("%w: %v", ErrPreviewSourceChanged, err)
	}
	return sourceBinding{info: info, size: info.Size(), modTime: info.ModTime()}, nil
}

func bindSourcePath(path string) (sourceBinding, error) {
	f, err := os.Open(path)
	if err != nil {
		return sourceBinding{}, fmt.Errorf("%w: %v", ErrPreviewSourceChanged, err)
	}
	defer f.Close()
	return newSourceBinding(f)
}

func sourceBindingFromEstimate(estimate previewEstimate) sourceBinding {
	if estimate.Source == nil {
		return sourceBinding{}
	}
	return *estimate.Source
}

func (b sourceBinding) valid() bool {
	return b.info != nil
}

// matches reports whether f still refers to the inspected file version. The
// descriptor, not a later path lookup, is authoritative for generation.
func (b sourceBinding) matches(f *os.File) bool {
	if !b.valid() {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	if !os.SameFile(b.info, info) {
		return false
	}
	if b.size != info.Size() {
		return false
	}
	return b.modTime.Equal(info.ModTime())
}

func (b sourceBinding) matchesPath(path string) bool {
	if !b.valid() {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !os.SameFile(b.info, info) {
		return false
	}
	if b.size != info.Size() {
		return false
	}
	return b.modTime.Equal(info.ModTime())
}

func openBoundSource(ctx context.Context, path string, binding sourceBinding) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPreviewSourceChanged, err)
	}
	if binding.valid() && !binding.matches(f) {
		_ = f.Close()
		return nil, fmt.Errorf("%w: source changed while queued", ErrPreviewSourceChanged)
	}
	if !binding.valid() {
		// Fixed-size preview types still get a before/after version check even
		// when their estimate did not need to read a header.
		if _, err := newSourceBinding(f); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}
