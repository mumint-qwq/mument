// Added for mument 007-C. Uses synthetic originals, real SQLite and loopback HTTP.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/stretchr/testify/require"
)

func writeMoveImage(t *testing.T, path string, seed int64) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	rng := rand.New(rand.NewSource(seed))
	_, err := rng.Read(img.Pix)
	require.NoError(t, err)
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var data bytes.Buffer
	require.NoError(t, jpeg.Encode(&data, img, &jpeg.Options{Quality: 92}))
	require.Greater(t, data.Len(), 256*1024)
	require.NoError(t, os.WriteFile(path, data.Bytes(), 0o644))
}

// moveHTTPServer can exercise the same handlers through a recorder on hosts
// that deny listening sockets. The default scale run still uses loopback.
type moveHTTPServer struct {
	URL    string
	client *http.Client
	close  func()
}

type moveHandlerTransport struct{ http.Handler }

func (transport moveHandlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	recorder := httptest.NewRecorder()
	transport.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}

func newMoveHTTPServer(handler http.Handler) *moveHTTPServer {
	if os.Getenv("MUMENT_MOVE_HANDLER_TRANSPORT") == "1" {
		return &moveHTTPServer{
			URL: "http://mument.test", client: &http.Client{Transport: moveHandlerTransport{handler}},
			close: func() {},
		}
	}
	server := httptest.NewServer(handler)
	return &moveHTTPServer{URL: server.URL, client: server.Client(), close: server.Close}
}

func ledgerPreviewServer(t *testing.T, env *moveIndexEnv) *moveHTTPServer {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH == "386" || runtime.GOARCH == "arm" {
		t.Skip("filesystem does not provide strong cache ledger observations")
	}
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(2, 128, filepath.Join(env.root, "previews")))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, preview.Shutdown(ctx))
	})
	server := newMoveHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, err := previewHandler(w, r, &Context{User: env.user})
		if err != nil {
			http.Error(w, err.Error(), status)
		}
	}))
	t.Cleanup(server.close)
	return server
}

func readMovePreview(t *testing.T, env *moveIndexEnv, server *moveHTTPServer, path, size string) []byte {
	t.Helper()
	q := url.Values{"source": {"src"}, "path": {path}, "size": {size}}
	response, err := server.client.Get(server.URL + "?" + q.Encode())
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, "%s: %s", path, string(data[:min(120, len(data), len(data))]))
	return data
}

func TestLedgerMoveHTTP(t *testing.T) {
	n := 3
	if raw := os.Getenv("MUMENT_MOVE_IMAGE_COUNT"); raw != "" {
		var err error
		n, err = strconv.Atoi(raw)
		require.NoError(t, err)
		require.Contains(t, []int{100, 1000}, n)
	}
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	items := make([]map[string]string, n)
	for i := range items {
		name := fmt.Sprintf("photo-%04d.jpg", i)
		writeMoveImage(t, env.path("src", "A/"+name), int64(i))
		items[i] = moveItem("src", "/A/"+name, "src", "/B/"+name)
	}
	env.index("src", "/A/", false)
	env.index("src", "/B/", false)
	stats := func() preview.PreviewStats { return preview.GetService().Stats() }
	images := make([][32]byte, n)
	start := time.Now()
	before := stats()
	for i, item := range items {
		data := readMovePreview(t, env, server, item["fromPath"], "small")
		config, err := jpeg.DecodeConfig(bytes.NewReader(data))
		require.NoError(t, err)
		require.Equal(t, 256, config.Width)
		require.Equal(t, 256, config.Height)
		images[i] = sha256.Sum256(data)
	}
	warm := stats()
	require.Equal(t, uint64(n), warm.FullDecodes-before.FullDecodes)
	warmMS := time.Since(start).Milliseconds()
	unique := map[[32]byte]bool{}
	for i, item := range items {
		require.False(t, unique[images[i]])
		unique[images[i]] = true
		require.Equal(t, images[i], sha256.Sum256(readMovePreview(t, env, server, item["fromPath"], "small")))
	}
	env.startProbes()
	indexStart := files.IndexMaintenanceStats()
	warm = stats()
	result := env.patch("move", items)
	// The same measurement source is compiled on A/B, where this field is absent.
	if ledger := reflect.ValueOf(stats()).FieldByName("LedgerMoves"); ledger.IsValid() {
		t.Logf("ledger_move_stats=%+v", ledger.Interface())
		if os.Getenv("MUMENT_MOVE_EXPECT_BASELINE") != "1" {
			require.Equal(t, uint64((n+63)/64), ledger.FieldByName("PrepareTransactions").Uint())
			require.Equal(t, uint64((n+63)/64), ledger.FieldByName("CompletionTransactions").Uint())
			require.Equal(t, uint64(n), ledger.FieldByName("IntentRows").Uint())
		}
	}
	require.Equal(t, http.StatusOK, result.Status)
	require.Equal(t, n, result.Succeeded)
	status, listed, _, getMS := env.get("src", "/B/")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, listed, n)
	start = time.Now()
	for i, item := range items {
		require.Equal(t, images[i], sha256.Sum256(readMovePreview(t, env, server, item["toPath"], "small")), item["toPath"])
	}
	rereadMS := time.Since(start).Milliseconds()
	env.waitIdle()
	final := stats()
	t.Logf("images=%d warm_ms=%d patch_ms=%.3f immediate_get_ms=%.3f reread_ms=%d reread_decodes=%d index_before=%+v index_after=%+v",
		n, warmMS, result.Ms, getMS, rereadMS, final.FullDecodes-warm.FullDecodes, indexStart, files.IndexMaintenanceStats())
	if os.Getenv("MUMENT_MOVE_EXPECT_BASELINE") == "1" {
		require.Equal(t, uint64(n), final.FullDecodes-warm.FullDecodes)
		return
	}
	require.Equal(t, warm.FullDecodes, final.FullDecodes)
	require.Equal(t, uint64(n), final.CacheHits-warm.CacheHits)

	// Replacing original bytes invalidates exactly this image, even with a
	// restored nanosecond mtime. The other entries keep their cached bytes.
	target := items[0]["toPath"]
	diskPath := env.path("src", target)
	stat, err := os.Stat(diskPath)
	require.NoError(t, err)
	next := filepath.Join(filepath.Dir(diskPath), "new.jpg")
	writeMoveImage(t, next, 891024)
	require.NoError(t, os.Chtimes(next, stat.ModTime(), stat.ModTime()))
	require.NoError(t, os.Rename(next, diskPath))
	require.NotEqual(t, images[0], sha256.Sum256(readMovePreview(t, env, server, target, "small")))
	require.Equal(t, final.FullDecodes+1, stats().FullDecodes)
}
