package previewwarmup

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

type RetentionSample struct {
	ID               string `json:"id"`
	Source           string `json:"source,omitempty"`
	Path             string `json:"path,omitempty"`
	Reason           string `json:"reason"`
	OriginReferenced bool   `json:"originReferenced"`
	References       int    `json:"references"`
	Unproven         int    `json:"unproven"`
	Capped           bool   `json:"capped"`
}
type RetentionPage struct {
	Next    string            `json:"next,omitempty"`
	Samples []RetentionSample `json:"samples"`
	Reasons map[string]uint64 `json:"reasons"`
}

// Retention is a read-only administrator audit. It never treats absence as an
// unlink proof. Each source stat is protected by the current configured scope
// and a durable pin; identities and original digests are not returned.
func (w *ReuseWorker) Retention(ctx context.Context, after string) (RetentionPage, error) {
	page := RetentionPage{Reasons: map[string]uint64{}}
	if w.repo == nil || !w.status.Enabled {
		return page, ErrInactive
	}
	rows, err := w.repo.ContentRetentionPage(ctx, after)
	if err != nil {
		return page, err
	}
	for _, r := range rows {
		if err = ctx.Err(); err != nil {
			return page, err
		}
		row := RetentionSample{ID: cacheledger.Digest([]byte(r.Cursor))[:16], OriginReferenced: r.OriginReferenced, References: r.References, Unproven: r.Unproven, Capped: r.References >= 17 || r.Unproven >= 17}
		row.Reason = "origin_mapping_unavailable"
		if r.FullPath != "" {
			row.Reason = "outside_current_scope"
			for i := range w.scopes {
				cfg := &w.scopes[i]
				if cfg.config.Source != r.SourceName {
					continue
				}
				rel, e := filepath.Rel(cfg.sourceRoot, r.FullPath)
				if e != nil || !filepath.IsLocal(rel) || !settings.WarmupContains(cfg.config.Directory, rel) {
					continue
				}
				// Do not access the scheduler's mutable runtime pin from the HTTP goroutine.
				s := scope{config: cfg.config, sourceRoot: cfg.sourceRoot, root: cfg.root, prefix: cfg.prefix}
				row.Source = r.SourceName
				row.Path = "/" + filepath.ToSlash(rel)
				if _, e = s.online(); e != nil {
					row.Reason = "scope_offline"
					break
				}
				rootID, e := diskcache.SourceRootIdentity(s.root)
				if e != nil {
					row.Reason = "scope_identity_unavailable"
					break
				}
				raw, _ := json.Marshal(rootID)
				known, e := w.repo.ContentScopeMatches(ctx, w.scopeKey(&s), string(raw))
				if e != nil {
					return page, e
				}
				if !known {
					row.Reason = "scope_identity_unavailable"
					break
				}
				if !r.OriginReferenced {
					row.Reason = "origin_ref_superseded"
					break
				}
				observed, e := diskcache.ObserveSource(s.root, r.FullPath)
				if e != nil {
					row.Reason = "missing_or_unreadable_unconfirmed"
					break
				}
				ns, name, ok := cacheledger.Location(s.sourceRoot, r.FullPath)
				matches := ok && ns == r.Ref.Source && name == r.Ref.Path && observed.Proof.RootID == rootID &&
					observed.Proof.Check() == nil && cacheledger.FromFileInfo(observed.Info) == r.Ref.Version
				observed.Close()
				row.Reason = "changed_unconfirmed"
				if matches {
					row.Reason = "live_verified_observation"
				}
				break
			}
		}
		page.Reasons[row.Reason]++
		page.Samples = append(page.Samples, row)
		page.Next = r.Cursor
	}
	if len(rows) < 16 {
		page.Next = ""
	}
	return page, nil
}

func ValidRetentionCursor(value string) bool {
	if len(value) > 128 {
		return false
	}
	return !strings.ContainsAny(value, "/\\\x00\r\n")
}
