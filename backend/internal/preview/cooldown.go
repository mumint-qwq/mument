package preview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

var ErrWarmupCooldown = errors.New("cooldown")
var ErrWarmupCapacityRecovery = errors.New("capacity_recovery")

type CooldownTarget struct {
	Path   string
	Info   os.FileInfo
	Cursor cacheledger.CooldownCursor
}

func (s *Service) warmupCooldown(ctx context.Context, plan *ledgerPreview) error {
	if s.capacity == nil {
		return nil
	}
	until, err := s.ledger.CoolingUntil(ctx, plan.ref.Asset, plan.variant)
	if err != nil {
		return ErrWarmupStore
	}
	if s.space.now().Before(until) {
		s.capacity.cooldownSkips.Add(1)
		return ErrWarmupCooldown
	}
	pending, err := s.ledger.CapacityRecoveryPending(ctx, plan.key)
	if err != nil {
		return ErrWarmupStore
	}
	if pending {
		return ErrWarmupCapacityRecovery
	}
	return nil
}

// WarmupCooldownPage feeds a bounded D lane without changing any index guard.
// A moved or unknown path waits for normal rediscovery or manual fill.
func (s *Service) WarmupCooldownPage(ctx context.Context, source string, after cacheledger.CooldownCursor) ([]CooldownTarget, cacheledger.CooldownCursor, error) {
	if s.capacity == nil {
		return nil, cacheledger.CooldownCursor{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	page, err := s.ledger.CooldownDue(ctx, source, after, s.space.now())
	if err != nil {
		return nil, after, err
	}
	var targets []CooldownTarget
	for _, entry := range page {
		if err := ctx.Err(); err != nil {
			return nil, after, err
		}
		target, ok := s.cooldownTarget(ctx, entry)
		if ok {
			targets = append(targets, target)
		}
	}
	var resume cacheledger.CooldownCursor
	if len(page) == cacheledger.OrphanPageSize {
		resume = page[len(page)-1].Cursor
	}
	return targets, resume, nil
}

func (s *Service) cooldownTarget(ctx context.Context, entry cacheledger.CooldownEntry) (CooldownTarget, bool) {
	var proof capacitySourceProof
	if json.Unmarshal([]byte(entry.Proof), &proof) != nil || proof.checkOnline() != nil {
		return CooldownTarget{}, false
	}
	var mime string
	switch strings.ToLower(filepath.Ext(proof.Path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".png":
		mime = "image/png"
	default:
		return CooldownTarget{}, false
	}
	file := iteminfo.ExtendedFileInfo{FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: filepath.Base(proof.Path), Type: mime}}}
	if ledgerVariant(file, warmupSize) != entry.Variant {
		return CooldownTarget{}, false
	}
	current, err := diskcache.ObserveSource(proof.Root, proof.Path)
	if err != nil {
		return CooldownTarget{}, false
	}
	defer current.Close()
	if current.Proof.RootID != proof.RootID || current.Proof.ParentID != proof.ParentID {
		return CooldownTarget{}, false
	}
	ns, name, ok := cacheledger.Location(proof.Root, proof.Path)
	if !ok {
		return CooldownTarget{}, false
	}
	ref, found, err := s.ledger.Existing(ctx, ns, name, cacheledger.FromFileInfo(current.Info))
	if err != nil || !found || ref.Asset != entry.Asset {
		return CooldownTarget{}, false
	}
	return CooldownTarget{Path: proof.Path, Info: current.Info, Cursor: entry.Cursor}, true
}
