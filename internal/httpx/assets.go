package httpx

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// assetIndex maps a public asset URL path (e.g. "/static/css/app.css") to a
// short content hash. Built once at boot. The template `asset` helper uses
// it to emit "/static/css/app.css?v=<hash>" so a content change invalidates
// every downstream cache (browser, service worker, Caddy, Cloudflare) the
// next time the page is rendered, without anyone having to bump a version.
type assetIndex struct {
	mu     sync.RWMutex
	hashes map[string]string
}

func newAssetIndex(webDir string) (*assetIndex, error) {
	idx := &assetIndex{hashes: map[string]string{}}
	staticDir := filepath.Join(webDir, "static")
	err := filepath.WalkDir(staticDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(staticDir, p)
		if relErr != nil {
			return relErr
		}
		h, hashErr := hashFile(p)
		if hashErr != nil {
			return hashErr
		}
		// Use forward slashes in the public URL even on platforms where
		// filepath.Rel returns backslashes.
		idx.hashes["/static/"+path.Clean(strings.ReplaceAll(rel, string(filepath.Separator), "/"))] = h
		return nil
	})
	if err != nil {
		return nil, err
	}
	return idx, nil
}

func (i *assetIndex) version(p string) string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.hashes[p]
}

// url returns the path with a ?v=<hash> suffix when the asset is known.
// Unknown paths pass through unchanged so templates don't break on typos
// or on assets that live outside /static/.
func (i *assetIndex) url(p string) string {
	v := i.version(p)
	if v == "" {
		return p
	}
	if strings.Contains(p, "?") {
		return p + "&v=" + v
	}
	return p + "?v=" + v
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	// 8 hex chars is enough to make collisions on a small static dir
	// effectively impossible while keeping URLs short.
	return hex.EncodeToString(h.Sum(nil))[:8], nil
}
