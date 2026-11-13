package apphost

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
)

// Errors one asset read answers with (KOSD-C-73).
var (
	// ErrAssetUnknown refuses a path the installed package does not ship.
	ErrAssetUnknown = errors.New("asset is not in the installed package")
	// ErrAssetUnaddressed refuses a request whose address does not name an
	// installed package version.
	ErrAssetUnaddressed = errors.New("asset address does not name an installed version")
)

// AssetStore answers the UI assets one installed package ships. The control plane
// owns the package; this seam is how a deployment hands the bytes to the process
// that serves the application origins.
type AssetStore interface {
	// Read returns one asset's bytes. A path the package does not ship is
	// ErrAssetUnknown; a version or digest the store does not hold is
	// ErrAssetUnaddressed.
	Read(ctx context.Context, asset Asset) ([]byte, error)
}

// MemoryAssets is an in-process asset store: a deployment publishes the files a
// verified package ships, and the front serves them under the versioned address.
// Publishing a version already held with other bytes or under another digest is
// refused, because a published address never changes content (KOSD-C-73).
type MemoryAssets struct {
	mu       sync.RWMutex
	packages map[string]memoryPackage
}

// memoryPackage is one published package version.
type memoryPackage struct {
	digest string
	files  map[string][]byte
}

// NewMemoryAssets builds an empty asset store.
func NewMemoryAssets() *MemoryAssets {
	return &MemoryAssets{packages: make(map[string]memoryPackage)}
}

// Publish stores the files of one package version and digest. The address names
// both, so publishing a version twice with other bytes or another digest is
// refused instead of silently changing what an open window loads (KOSD-C-73).
func (m *MemoryAssets) Publish(extension, version, digest string, files map[string][]byte) error {
	if extension == "" || version == "" || digest == "" {
		return fmt.Errorf("apphost: publish assets for an unnamed extension, version or digest")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := packageKey(extension, version)
	if existing, found := m.packages[key]; found {
		if existing.digest != digest || !sameFiles(existing.files, files) {
			return fmt.Errorf("apphost: %s %s already published with other content", extension, version)
		}
		return nil
	}
	stored := make(map[string][]byte, len(files))
	for name, content := range files {
		stored[normalizeAssetPath(name)] = append([]byte(nil), content...)
	}
	m.packages[key] = memoryPackage{digest: digest, files: stored}
	return nil
}

// Read returns one asset the published version ships. A digest other than the
// published one names no installed package (KOSD-C-73).
func (m *MemoryAssets) Read(_ context.Context, asset Asset) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	published, found := m.packages[packageKey(asset.Extension, asset.Version)]
	if !found || published.digest != asset.Digest {
		return nil, ErrAssetUnaddressed
	}
	content, found := published.files[normalizeAssetPath(asset.Path)]
	if !found {
		return nil, ErrAssetUnknown
	}
	return append([]byte(nil), content...), nil
}

// DirAssets serves UI assets from a deployment directory laid out as
// root/<extension>/<application>/<version>/<digest>/<asset path>, the same
// segments Asset.AddressPath names, so the served address and this store always
// point at one place. The application segment keeps two applications of one
// package apart, the digest keeps a new build from overwriting the bytes an open
// window loads, and a path that would leave the root is never served
// (KOSD-C-73).
type DirAssets struct {
	// Root is the directory the verified packages were unpacked into; required.
	Root string
}

// Read returns one asset's bytes from the unpacked package.
func (d *DirAssets) Read(_ context.Context, asset Asset) ([]byte, error) {
	if d == nil || d.Root == "" {
		return nil, ErrAssetUnaddressed
	}
	for _, segment := range []string{asset.Extension, asset.AppID, asset.Version, asset.Digest} {
		if !isSafeSegment(segment) {
			return nil, ErrAssetUnknown
		}
	}
	assetPath := strings.TrimPrefix(path.Clean("/"+asset.Path), "/")
	if assetPath == "" || assetPath == "." || strings.HasPrefix(assetPath, "../") {
		return nil, ErrAssetUnknown
	}
	relative := asset.Extension + "/" + asset.AppID + "/" + asset.Version + "/" + asset.Digest + "/" + assetPath
	content, err := fs.ReadFile(os.DirFS(d.Root), relative)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if info, statErr := os.Stat(d.Root); statErr == nil && info.IsDir() {
			if _, versionErr := os.Stat(joinUnder(d.Root, asset.Extension, asset.AppID, asset.Version, asset.Digest)); versionErr != nil {
				return nil, ErrAssetUnaddressed
			}
		}
		return nil, ErrAssetUnknown
	case err != nil:
		return nil, err
	}
	return content, nil
}

// joinUnder joins the segments under one root for an existence check.
func joinUnder(root string, segments ...string) string {
	joined := root
	for _, segment := range segments {
		joined = joined + string(os.PathSeparator) + segment
	}
	return joined
}

// isSafeSegment reports a single path segment: a name, never a traversal.
func isSafeSegment(segment string) bool {
	return segment != "" && segment != "." && segment != ".." &&
		!strings.ContainsAny(segment, `/\`) && !strings.Contains(segment, "\x00")
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
	for name, content := range right {
		other, found := left[normalizeAssetPath(name)]
		if !found || string(other) != string(content) {
			return false
		}
	}
	return true
}
