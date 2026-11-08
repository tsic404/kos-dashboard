package apphost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// The permission vocabulary is closed (KOSD-C-75): an application may use these
// names and nothing else, and a manifest naming any other value is invalid, so
// the launcher never offers an entry whose capability set the console cannot
// enforce.
const (
	// PermissionNotifications lets the application post desktop notifications.
	PermissionNotifications = "notifications"
	// PermissionClipboardRead lets the application read the clipboard.
	PermissionClipboardRead = "clipboard-read"
	// PermissionClipboardWrite lets the application write the clipboard.
	PermissionClipboardWrite = "clipboard-write"
	// PermissionFilePicker lets the application ask the console for a file.
	PermissionFilePicker = "file-picker"
	// PermissionExternalLink lets the application open an external address.
	PermissionExternalLink = "external-link"
)

// permissionNames is the fixed set KOSD-C-75 declares.
var permissionNames = []string{
	PermissionNotifications,
	PermissionClipboardRead,
	PermissionClipboardWrite,
	PermissionFilePicker,
	PermissionExternalLink,
}

// Window modes an application may declare (DASH-C-74).
const (
	// WindowModeSingle focuses the open window instead of opening another one.
	WindowModeSingle = "single"
	// WindowModeMulti opens one window per deep link.
	WindowModeMulti = "multi"
)

// Default window states a manifest may declare (DASH-C-74).
const (
	// DefaultStateNormal opens at the declared default size.
	DefaultStateNormal = "normal"
	// DefaultStateMaximized opens maximized.
	DefaultStateMaximized = "maximized"
	// DefaultStateFullscreen opens fullscreen.
	DefaultStateFullscreen = "fullscreen"
)

// appIDPattern keeps an application identity usable in a URL path and a
// preference key: lower-case letters, digits, dots and dashes.
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// Manifest is the `App` contribution KOSD-C-73 fixes: the fields a window
// application declares in its extension package, and the only ones the console
// uses to list, open and embed it.
type Manifest struct {
	// ID is the application identity, unique among the installed extensions.
	ID string `json:"id"`
	// Name is the label the launcher and dock show.
	Name string `json:"name"`
	// Version is the application version this manifest belongs to; UI assets
	// are addressed by it (KOSD-C-73).
	Version string `json:"version"`
	// Icon is the icon reference: an absolute URL on the application origin, or
	// a package-relative asset path.
	Icon string `json:"icon"`
	// Entry is the HTTPS address of the application document on its own origin.
	Entry string `json:"entry"`
	// Window carries the window defaults and the instance mode (DASH-C-74).
	Window WindowDefaults `json:"window"`
	// Permissions is the declared capability set; empty is the same as absent.
	Permissions []string `json:"permissions"`
	// Backend is the application's own service, absent when the application is
	// UI-only.
	Backend *Backend `json:"backend,omitempty"`
	// DeepLinks are the path prefixes the desktop resolves to this application;
	// empty means no deep link opens it.
	DeepLinks []string `json:"deepLinks,omitempty"`
}

// Window is the window default an `App` declares (KOSD-C-73): the size the
// console opens it at, the size it may not shrink below, whether the user may
// resize it, the instance mode and the state it opens in.
type WindowDefaults struct {
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	MinWidth     int    `json:"minWidth"`
	MinHeight    int    `json:"minHeight"`
	Resizable    bool   `json:"resizable"`
	Mode         string `json:"mode"`
	DefaultState string `json:"defaultState"`
}

// Backend is the optional application service an `App` declares (KOSD-C-73).
type Backend struct {
	// Type is the protocol family the service speaks.
	Type string `json:"type"`
	// Health is the address the console probes.
	Health string `json:"health"`
}

// manifestWire is the decode target: it keeps the presence of every required
// field apart from its zero value, so a missing size is told from a zero one.
type manifestWire struct {
	ID          *string     `json:"id"`
	Name        *string     `json:"name"`
	Version     *string     `json:"version"`
	Icon        *string     `json:"icon"`
	Entry       *string     `json:"entry"`
	Window      *windowWire `json:"window"`
	Permissions []string    `json:"permissions"`
	Backend     *Backend    `json:"backend"`
	DeepLinks   []string    `json:"deepLinks"`
}

// windowWire keeps the presence of each required window field.
type windowWire struct {
	Width        *int    `json:"width"`
	Height       *int    `json:"height"`
	MinWidth     *int    `json:"minWidth"`
	MinHeight    *int    `json:"minHeight"`
	Resizable    *bool   `json:"resizable"`
	Mode         *string `json:"mode"`
	DefaultState *string `json:"defaultState"`
}

// DecodeManifest reads one `App` contribution. Unknown fields are refused: a
// manifest is a contract, and a field the console does not understand would
// otherwise be dropped silently.
func DecodeManifest(raw []byte) (Manifest, error) {
	var wire manifestWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Manifest{}, fmt.Errorf("decode app manifest: %w", err)
	}
	// Presence is judged here, values are judged by Validate: a required field
	// that is absent is not the same refusal as one that is wrong.
	for field, present := range map[string]bool{
		"id": wire.ID != nil, "name": wire.Name != nil, "version": wire.Version != nil,
		"icon": wire.Icon != nil, "entry": wire.Entry != nil, "window": wire.Window != nil,
		"permissions": wire.Permissions != nil,
	} {
		if !present {
			return Manifest{}, fmt.Errorf("app manifest declares no %s", field)
		}
	}
	window := wire.Window
	for field, present := range map[string]bool{
		"width": window.Width != nil, "height": window.Height != nil,
		"minWidth": window.MinWidth != nil, "minHeight": window.MinHeight != nil,
		"resizable": window.Resizable != nil, "mode": window.Mode != nil,
		"defaultState": window.DefaultState != nil,
	} {
		if !present {
			return Manifest{}, fmt.Errorf("app manifest window declares no %s", field)
		}
	}
	manifest := Manifest{Permissions: wire.Permissions, DeepLinks: wire.DeepLinks}
	if wire.ID != nil {
		manifest.ID = *wire.ID
	}
	if wire.Name != nil {
		manifest.Name = *wire.Name
	}
	if wire.Version != nil {
		manifest.Version = *wire.Version
	}
	if wire.Icon != nil {
		manifest.Icon = *wire.Icon
	}
	if wire.Entry != nil {
		manifest.Entry = *wire.Entry
	}
	if wire.Window != nil {
		manifest.Window = WindowDefaults{
			Width:        *window.Width,
			Height:       *window.Height,
			MinWidth:     *window.MinWidth,
			MinHeight:    *window.MinHeight,
			Resizable:    *window.Resizable,
			Mode:         *window.Mode,
			DefaultState: *window.DefaultState,
		}
	}
	manifest.Backend = wire.Backend
	return manifest, nil
}

// ValidationOptions carries what a manifest is judged against that the manifest
// itself cannot state.
type ValidationOptions struct {
	// ConsoleOrigin is the origin the console is served from; an application
	// entry may never share it (KOSD-F-18).
	ConsoleOrigin string
	// AllowInsecureEntries admits plain-HTTP entries on a loopback host, which
	// is how a local deployment serves an application. A non-loopback entry
	// must be HTTPS whatever this says (KOSD-C-73).
	AllowInsecureEntries bool
}

// Validate judges one manifest against the required set KOSD-C-73 fixes. The
// first failure is returned: a manifest either satisfies the set or it is
// invalid, and an invalid declaration makes the component not ready.
func (m Manifest) Validate(options ValidationOptions) error {
	if !appIDPattern.MatchString(m.ID) {
		return fmt.Errorf("app id %q is not lower-case letters, digits, dots and dashes", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("app %s declares no name", m.ID)
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("app %s declares no version", m.ID)
	}
	if m.Entry == "" {
		return fmt.Errorf("app %s declares no entry", m.ID)
	}
	entry, err := url.Parse(m.Entry)
	if err != nil || entry.Host == "" {
		return fmt.Errorf("app %s entry %q is not an absolute URL", m.ID, m.Entry)
	}
	if issue := entryScheme(entry, options.AllowInsecureEntries); issue != "" {
		return fmt.Errorf("app %s entry %q %s", m.ID, m.Entry, issue)
	}
	if options.ConsoleOrigin != "" && sameOrigin(entry, options.ConsoleOrigin) {
		return fmt.Errorf("app %s entry shares the console origin %s", m.ID, options.ConsoleOrigin)
	}
	if m.Icon == "" {
		return fmt.Errorf("app %s declares no icon", m.ID)
	}
	if err := m.validateIcon(entry); err != nil {
		return err
	}
	if err := m.Window.validate(m.ID); err != nil {
		return err
	}
	declared := make(map[string]bool, len(m.Permissions))
	for _, permission := range m.Permissions {
		if !isPermission(permission) {
			return fmt.Errorf("app %s declares unknown permission %q", m.ID, permission)
		}
		if declared[permission] {
			return fmt.Errorf("app %s declares permission %q twice", m.ID, permission)
		}
		declared[permission] = true
	}
	if m.Backend != nil {
		if strings.TrimSpace(m.Backend.Type) == "" {
			return fmt.Errorf("app %s backend declares no type", m.ID)
		}
		if m.Backend.Health == "" {
			return fmt.Errorf("app %s backend declares no health address", m.ID)
		}
		health, err := url.Parse(m.Backend.Health)
		if err != nil || health.Host == "" {
			return fmt.Errorf("app %s health %q is not an absolute URL", m.ID, m.Backend.Health)
		}
		if issue := entryScheme(health, options.AllowInsecureEntries); issue != "" {
			return fmt.Errorf("app %s health %q %s", m.ID, m.Backend.Health, issue)
		}
	}
	for _, link := range m.DeepLinks {
		if !strings.HasPrefix(link, "/") || strings.HasPrefix(link, "//") {
			return fmt.Errorf("app %s deep link %q is not a path", m.ID, link)
		}
	}
	return nil
}

// validate checks one window declaration: every field is required, the minimum
// never exceeds the default and the two enumerations are closed.
func (w WindowDefaults) validate(id string) error {
	if w.Width <= 0 || w.Height <= 0 {
		return fmt.Errorf("app %s declares no positive default size", id)
	}
	if w.MinWidth <= 0 || w.MinHeight <= 0 {
		return fmt.Errorf("app %s declares no positive minimum size", id)
	}
	if w.MinWidth > w.Width || w.MinHeight > w.Height {
		return fmt.Errorf("app %s minimum size exceeds its default size", id)
	}
	switch w.Mode {
	case WindowModeSingle, WindowModeMulti:
	default:
		return fmt.Errorf("app %s window mode %q is neither %s nor %s",
			id, w.Mode, WindowModeSingle, WindowModeMulti)
	}
	switch w.DefaultState {
	case DefaultStateNormal, DefaultStateMaximized, DefaultStateFullscreen:
	default:
		return fmt.Errorf("app %s window default state %q is not one of %s, %s, %s",
			id, w.DefaultState, DefaultStateNormal, DefaultStateMaximized, DefaultStateFullscreen)
	}
	return nil
}

// validateIcon judges the icon source: a package-relative asset or a URL on the
// application's own origin. Any other source is not loaded (KOSD-C-73).
func (m Manifest) validateIcon(entry *url.URL) error {
	if !strings.Contains(m.Icon, "://") {
		if strings.HasPrefix(m.Icon, "//") || strings.HasPrefix(m.Icon, "/") {
			return fmt.Errorf("app %s icon %q is not a package-relative asset", m.ID, m.Icon)
		}
		return nil
	}
	icon, err := url.Parse(m.Icon)
	if err != nil || icon.Host == "" {
		return fmt.Errorf("app %s icon %q is not an absolute URL", m.ID, m.Icon)
	}
	if !strings.EqualFold(icon.Scheme, entry.Scheme) || !strings.EqualFold(icon.Host, entry.Host) {
		return fmt.Errorf("app %s icon %q is not on the application origin %s",
			m.ID, m.Icon, entry.Scheme+"://"+entry.Host)
	}
	return nil
}

// Origin is the application's own origin: scheme and host, never a path
// prefix shared with the console or another application (KOSD-C-73, KOSD-F-18).
func (m Manifest) Origin() string {
	entry, err := url.Parse(m.Entry)
	if err != nil || entry.Host == "" {
		return ""
	}
	return strings.ToLower(entry.Scheme + "://" + entry.Host)
}

// ResolveDeepLink reports whether a path is one the application declared.
func (m Manifest) ResolveDeepLink(path string) bool {
	for _, link := range m.DeepLinks {
		if path == link || strings.HasPrefix(path, strings.TrimSuffix(link, "/")+"/") {
			return true
		}
	}
	return false
}

// isPermission reports whether a name belongs to the fixed set (KOSD-C-75).
func isPermission(name string) bool {
	for _, candidate := range permissionNames {
		if candidate == name {
			return true
		}
	}
	return false
}

// entryScheme judges the transport of an application address: HTTPS, or plain
// HTTP only on a loopback host while the deployment allows it.
func entryScheme(parsed *url.URL, allowInsecure bool) string {
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "https" {
		return ""
	}
	if scheme == "http" && allowInsecure && isLoopback(parsed.Hostname()) {
		return ""
	}
	return "is not an HTTPS address on an independent origin"
}

// isLoopback reports a host a browser treats as a secure context.
func isLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return strings.HasSuffix(strings.ToLower(host), ".localhost")
	}
}

// sameOrigin reports whether two absolute URLs share scheme and host.
func sameOrigin(left *url.URL, right string) bool {
	other, err := url.Parse(right)
	if err != nil || other.Host == "" {
		return false
	}
	return strings.EqualFold(left.Scheme, other.Scheme) && strings.EqualFold(left.Host, other.Host)
}

// ErrNoDeepLink is the refusal for a path no installed application declared.
var ErrNoDeepLink = errors.New("no installed application declares this deep link")
