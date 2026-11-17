// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
package web

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/share"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func TestErrToStatusUnsupportedPreviewFormat(t *testing.T) {
	status := ErrToStatus(preview.ErrUnsupportedFormat)
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("ErrToStatus() = %d, want %d", status, http.StatusUnsupportedMediaType)
	}
}

func TestErrToStatusWrapsUnsupportedPreviewFormat(t *testing.T) {
	err := errors.Join(errors.New("preview failed"), preview.ErrUnsupportedFormat)
	status := ErrToStatus(err)
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("ErrToStatus() = %d, want %d", status, http.StatusUnsupportedMediaType)
	}
}

func TestResolveDirectoryPreviewLocationPreservesScopedAndSharePaths(t *testing.T) {
	previousSourceMap := settings.Config.Server.SourceMap
	previousNameMap := settings.Config.Server.NameToSource
	t.Cleanup(func() {
		settings.Config.Server.SourceMap = previousSourceMap
		settings.Config.Server.NameToSource = previousNameMap
		settings.InitializeUserResolvers()
	})

	source := &settings.Source{Path: "/srv/pictures", Name: "pictures"}
	settings.Config.Server.SourceMap = map[string]*settings.Source{source.Path: source}
	settings.Config.Server.NameToSource = map[string]*settings.Source{source.Name: source}
	settings.InitializeUserResolvers()

	owner := &users.User{
		FrontendUser: users.FrontendUser{
			Username: "owner",
			NonAdminEditable: users.NonAdminEditable{
				ShowHidden: true, HideFileExt: ".tmp",
			},
		},
		BackendScopes: []users.BackendScope{{Path: source.Path, Scope: "/Pictures/"}},
	}
	authLocation, err := resolveDirectoryPreviewLocation(&Context{
		User: owner,
		FileInfo: iteminfo.ExtendedFileInfo{
			FileInfo: iteminfo.FileInfo{Path: "/Trips/"}, Source: source.Name,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if authLocation.requestPath != "/Trips/" || authLocation.indexPath != "/Pictures/Trips/" {
		t.Fatalf("authenticated location = %#v", authLocation)
	}
	if !authLocation.showHidden || authLocation.hideFileExt != ".tmp" {
		t.Fatalf("authenticated filters = %#v", authLocation)
	}

	shareLocation, err := resolveDirectoryPreviewLocation(&Context{
		User: owner, ShareUser: owner, IndexPath: "/Pictures/Shared/",
		Share: share.Share{
			ShareColumns: share.ShareColumns{Hash: "hash"},
			SourcePath:   source.Path,
			ShareSettings: share.ShareSettings{
				FrontendShareInfo: share.FrontendShareInfo{ShowHidden: true},
				ShareLimits:       share.ShareLimits{HideFileExt: ".bak"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if shareLocation.source != source.Name || shareLocation.requestPath != "/Pictures/Shared/" || shareLocation.indexPath != "/Pictures/Shared/" {
		t.Fatalf("share location = %#v", shareLocation)
	}
	if !shareLocation.followSymlinks || !shareLocation.showHidden || shareLocation.hideFileExt != ".bak" {
		t.Fatalf("share filters = %#v", shareLocation)
	}
}

func TestLiveDirectoryPreviewPathsKeepsOnlyFourPreviewableChildren(t *testing.T) {
	original := files.FileInfoFasterFunc
	t.Cleanup(func() { files.FileInfoFasterFunc = original })

	var captured utils.FileOptions
	files.FileInfoFasterFunc = func(opts utils.FileOptions, _ *users.User) (*iteminfo.ExtendedFileInfo, error) {
		captured = opts
		children := []iteminfo.ExtendedItemInfo{
			{ItemInfo: iteminfo.ItemInfo{Name: "notes.txt", Type: "text/plain", HasPreview: true}},
		}
		for _, name := range []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg", "e.jpg"} {
			children = append(children, iteminfo.ExtendedItemInfo{ItemInfo: iteminfo.ItemInfo{
				Name: name, Type: "image/jpeg", HasPreview: true, ModTime: time.Unix(1, 0),
			}})
		}
		return &iteminfo.ExtendedFileInfo{FileInfo: iteminfo.FileInfo{Files: children}}, nil
	}

	paths, err := liveDirectoryPreviewPaths(directoryPreviewLocation{
		source: "pictures", requestPath: "/album/", user: &users.User{},
		showHidden: true, hideFileExt: ".tmp", followSymlinks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(paths), directoryPreviewFrames; got != want {
		t.Fatalf("candidate count = %d, want %d (%#v)", got, want, paths)
	}
	if paths[0] != "a.jpg" || paths[3] != "d.jpg" {
		t.Fatalf("candidate names = %#v", paths)
	}
	if !captured.Expand || !captured.ShowHidden || !captured.FollowSymlinks || captured.HideFileExt != ".tmp" {
		t.Fatalf("fallback options = %#v", captured)
	}
}
