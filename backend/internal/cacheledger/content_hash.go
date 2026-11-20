package cacheledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// HashContent reads the same regular descriptor to EOF with one fixed buffer.
// Callers revalidate path, scope, and reference token before persisting evidence.
// pace executes at every chunk boundary outside repository locks/transactions.
func HashContent(ctx context.Context, f *os.File, v Version, maximum int64, pace func(context.Context, int) error) (ContentProof, int64, error) {
	if !v.Strong || v.Size <= 0 || v.Size > maximum {
		return ContentProof{}, 0, ErrStale
	}
	initial, err := f.Stat()
	if err != nil {
		return ContentProof{}, 0, err
	}
	if !initial.Mode().IsRegular() || FromFileInfo(initial) != v {
		return ContentProof{}, 0, ErrStale
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return ContentProof{}, 0, err
	}
	h := sha256.New()
	buf := make([]byte, 256<<10)
	var total int64
	for {
		if err = ctx.Err(); err != nil {
			return ContentProof{}, total, err
		}
		if pace != nil {
			if err = pace(ctx, 0); err != nil {
				return ContentProof{}, total, err
			}
		}
		n, e := f.Read(buf[:min(int64(len(buf)), v.Size-total+1)])
		if n > 0 {
			total += int64(n)
			if total > v.Size {
				return ContentProof{}, total, ErrStale
			}
			_, _ = h.Write(buf[:n])
			if pace != nil {
				if err = pace(ctx, n); err != nil {
					return ContentProof{}, total, err
				}
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return ContentProof{}, total, e
		}
		if n == 0 {
			return ContentProof{}, total, io.ErrNoProgress
		}
	}
	after, err := f.Stat()
	if err != nil {
		return ContentProof{}, total, err
	}
	if total != v.Size || FromFileInfo(after) != v {
		return ContentProof{}, total, ErrStale
	}
	return ContentProof{Digest: hex.EncodeToString(h.Sum(nil)), Bytes: total}, total, nil
}
