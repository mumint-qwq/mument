//go:build linux

package cachetrial

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

const fixtureGrace = 7 * 24 * time.Hour

type fixtureRef struct {
	Asset, Source, Path, Version string
}

type observation struct {
	State     string
	At        time.Time
	Reference *fixtureRef
	current   bool
}

type evidence struct {
	Scope        string
	Publications map[string]observation
	Unregistered []string
}

func readEvidence(ctx context.Context, db *sql.DB, path string, now time.Time) (evidence, error) {
	var data evidence
	if path == "" {
		return data, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return data, errors.New("fixture evidence must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return data, errors.New("cannot open fixture evidence")
	}
	defer f.Close()
	const maxEvidenceBytes = 128 << 10
	raw, err := io.ReadAll(io.LimitReader(f, maxEvidenceBytes+1))
	if err != nil || len(raw) > maxEvidenceBytes || json.Unmarshal(raw, &data) != nil ||
		data.Scope != "synthetic" || len(data.Publications) > 128 || len(data.Unregistered) > 128 {
		return data, errors.New("invalid synthetic fixture evidence")
	}
	for id, obs := range data.Publications {
		if !lowerHex(id, 64) || obs.At.IsZero() || obs.At.After(now) {
			return data, errors.New("invalid fixture publication identity or observation time")
		}
		switch obs.State {
		case "version_current":
			ref := obs.Reference
			if ref == nil {
				return data, errors.New("current fixture evidence requires an exact reference")
			}
			err := db.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM refs WHERE source=? AND path=? AND version=? AND asset=? )`,
				ref.Source, ref.Path, ref.Version, ref.Asset).Scan(&obs.current)
			if err != nil {
				return data, err
			}
			data.Publications[id] = obs
		case "version_retired", "no_reference", "offline", "identity_changed", "permission",
			"scan_incomplete", "index_stale":
			if obs.Reference != nil {
				return data, errors.New("unexpected reference in fixture observation")
			}
		default:
			return data, errors.New("invalid fixture observation state")
		}
	}
	for _, id := range data.Unregistered {
		if !lowerHex(id, 64) {
			return data, errors.New("invalid fixture file identity")
		}
	}
	return data, nil
}

func (p publication) id() string {
	identity, _ := json.Marshal([]any{p.asset, p.variant, p.key, p.size, p.digest, p.generated})
	return anonymous(string(identity))
}

func anonymous(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Fixture estimates match the current bytes against the publication. Ordinary
// inventory never opens a preview for data reads.
func verifyFixtureBytes(ctx context.Context, root int, relative string, expected unix.Stat_t, digest string) bool {
	if ctx.Err() != nil || !lowerHex(digest, 64) {
		return false
	}
	path, err := unix.Openat2(root, relative, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: noSubmount,
	})
	if err != nil {
		return false
	}
	defer unix.Close(path)
	var before, after unix.Stat_t
	if unix.Fstat(path, &before) != nil || !sameFileVersion(expected, before) {
		return false
	}
	f, err := os.OpenFile("/proc/self/fd/"+strconv.Itoa(path), os.O_RDONLY|unix.O_NOATIME, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	buffer := make([]byte, 32<<10)
	remaining := expected.Size
	for remaining > 0 {
		if ctx.Err() != nil {
			return false
		}
		n, err := io.ReadFull(f, buffer[:min(int64(len(buffer)), remaining)])
		if err != nil {
			return false
		}
		h.Write(buffer[:n])
		remaining -= int64(n)
	}
	return unix.Fstat(path, &after) == nil && sameFileVersion(before, after) &&
		hex.EncodeToString(h.Sum(nil)) == digest
}

func sameFileVersion(a, b unix.Stat_t) bool {
	return a.Mode == b.Mode && a.Dev == b.Dev && a.Ino == b.Ino && a.Nlink == b.Nlink &&
		a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func keyMatchesPublication(p publication) bool {
	return p.key == "c-raster/"+p.asset+"/"+p.variant
}
