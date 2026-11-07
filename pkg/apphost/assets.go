package apphost

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
)

// Errors one asset read answers with (KOSD-C-73).
var (
	// ErrAssetUnknown refuses a path the installed package does not ship.
	ErrAssetUnknown = errors.New("asset is not in the installed package")
	// ErrAssetUnaddressed refuses a request whose address does not name the
	// installed version and digest.
	ErrAssetUnaddressed = errors.New("asset address does not name an installed version")
)

// AssetStore answers the UI assets one installed package version ships. The
// control plane owns the package; this seam is how a deployment hands the bytes
// to the process that serves the application origin.
type AssetStore interface {
	// Read returns one asset's bytes. A path the package does not ship is
	// ErrAssetUnknown; a version the store does not hold is
	// ErrAssetUnaddressed.
	Read(ctx context.Context, extension, version, assetPath string) ([]byte, error)
}

// MemoryAssets is an in-process asset store: a deployment publishes the files a
// verified package ships, keyed by extension identity and version, and the front
// serves them under the versioned address. A second publish of the same version
// with other bytes is refused, because a published address never changes content
// (KOSD-C-73).
type MemoryAssets struct {
	mu       sync.RWMutex
	packages map[string]map[string][]byte
}

// NewMemoryAssets builds an empty asset store.
func NewMemoryAssets() *MemoryAssets {
	return &MemoryAssets{packages: make(map[string]map[string][]byte)}
}

// Publish stores the files of one package version. The address names the
// extension and version, so publishing the same pair twice with different bytes
// is refused instead of silently changing what an open window loads.
func (m *MemoryAssets) Publish(extension, version string, files map[string][]byte) error {
	if extension == "" || version == "" {
		return fmt.Errorf("apphost: publish assets for an unnamed extension or version")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := packageKey(extension, version)
	if existing, found := m.packages[key]; found {
		if !sameFiles(existing, files) {
			return fmt.Errorf("apphost: %s %s already published with other content", extension, version)
		}
		return nil
	}
	stored := make(map[string][]byte, len(files))
	for name, content := range files {
		stored[normalizeAssetPath(name)] = append([]byte(nil), content...)
	}
	m.packages[key] = stored
	return nil
}

// Read returns one asset the published version ships.
func (m *MemoryAssets) Read(_ context.Context, extension, version, assetPath string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	files, found := m.packages[packageKey(extension, version)]
	if !found {
		return nil, ErrAssetUnaddressed
	}
	content, found := files[normalizeAssetPath(assetPath)]
	if !found {
		return nil, ErrAssetUnknown
	}
	return append([]byte(nil), content...), nil
}

// packageKey names one published package version.
func packageKey(extension, version string) string {
	return extension + "\x00" + version
}

// normalizeAssetPath trims the address form to the stored path form.
func normalizeAssetPath(assetPath string) string {
	return strings.TrimPrefix(path.Clean("/"+assetPath), "/")
}

// sameFiles reports whether two publishes carry the same bytes.
func sameFiles(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for name, content := range left {
		other, found := right[normalizeAssetPath(name)]
		if !found || string(other) != string(content) {
			return false
		}
	}
	return true
}
