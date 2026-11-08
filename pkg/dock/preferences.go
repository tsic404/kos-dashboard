package dock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The preference domains and keys the desktop declares (DASH-C-27). Both the
// dock and the launcher keep their account-scoped layout here; the launcher's
// recent list stays in browser storage and never enters this domain (C-53).
const (
	// PreferenceLauncher is the launcher preference domain.
	PreferenceLauncher = "launcher"
	// PreferenceLauncherLayout is the launcher layout key.
	PreferenceLauncherLayout = "listing"
)

// PreferenceLayoutVersion is the schema version both layout values carry. A
// value of another version is not read: DASH-C-27 migrates a known older one
// and treats an unsupported one as the default.
const PreferenceLayoutVersion = 1

// Dock positions DASH-C-30's dock declares.
const (
	// DockPositionBottom places the dock along the bottom edge.
	DockPositionBottom = "bottom"
	// DockPositionLeft places it on the left edge.
	DockPositionLeft = "left"
	// DockPositionRight places it on the right edge.
	DockPositionRight = "right"
)

// DockLayout is the account's dock layout (DASH-C-27, DASH-C-30): where the
// dock sits, whether it hides itself, and the applications pinned to it in
// order. The pinned list names application identities; an identity no
// installed application declares stays stored but is not shown, so a reinstall
// restores the same dock.
type DockLayout struct {
	// SchemaVersion is the value version.
	SchemaVersion int `json:"schemaVersion"`
	// Position is the dock edge (DASH-C-30).
	Position string `json:"position"`
	// AutoHide hides the dock until the pointer reaches its edge.
	AutoHide bool `json:"autoHide"`
	// Pinned is the pinned application order.
	Pinned []string `json:"pinned"`
}

// LauncherLayout is the account's launcher layout (DASH-C-27): how applications
// are grouped and ordered. Search terms, the open window and the recent list
// are not preferences and never enter this value.
type LauncherLayout struct {
	// SchemaVersion is the value version.
	SchemaVersion int `json:"schemaVersion"`
	// Sort is the listing order: name or recent.
	Sort string `json:"sort"`
	// Categories is the grouping order.
	Categories []string `json:"categories"`
}

// Launcher sort orders.
const (
	// LauncherSortName lists applications by name.
	LauncherSortName = "name"
	// LauncherSortRecentlyUsed lists the most recently opened first; the
	// recency itself is the browser-local recent list.
	LauncherSortRecentlyUsed = "recent"
)

// maxPinnedApps bounds one dock layout, so a stored value cannot grow without
// limit.
const maxPinnedApps = 64

// maxCategories bounds one launcher layout.
const maxCategories = 32

// DefaultDockLayout is the layout a value the account never wrote answers with.
func DefaultDockLayout() DockLayout {
	return DockLayout{SchemaVersion: PreferenceLayoutVersion, Position: DockPositionBottom}
}

// DefaultLauncherLayout is the launcher layout a value the account never wrote
// answers with.
func DefaultLauncherLayout() LauncherLayout {
	return LauncherLayout{SchemaVersion: PreferenceLayoutVersion, Sort: LauncherSortName}
}

// DecodeDockLayout reads one stored dock layout. An unreadable or unsupported
// value reads as the default and is left in place until the account writes
// again (DASH-C-27).
func DecodeDockLayout(value string) DockLayout {
	layout, err := parseDockLayout(value)
	if err != nil {
		return DefaultDockLayout()
	}
	return layout
}

// ParseDockLayout reads and validates one stored dock layout; a write goes
// through it, so a value the caller cannot honour is refused instead of stored.
func ParseDockLayout(value string) (DockLayout, error) {
	return parseDockLayout(value)
}

// parseDockLayout reads one value and judges it.
func parseDockLayout(value string) (DockLayout, error) {
	if strings.TrimSpace(value) == "" {
		return DefaultDockLayout(), nil
	}
	layout := DockLayout{}
	if err := strictDecode(value, &layout); err != nil {
		return DockLayout{}, fmt.Errorf("dock: read layout: %w", err)
	}
	if layout.SchemaVersion != PreferenceLayoutVersion {
		return DockLayout{}, fmt.Errorf("dock: layout schemaVersion %d is not supported", layout.SchemaVersion)
	}
	switch layout.Position {
	case DockPositionBottom, DockPositionLeft, DockPositionRight:
	default:
		return DockLayout{}, fmt.Errorf("dock: position %q is not bottom, left or right", layout.Position)
	}
	if len(layout.Pinned) > maxPinnedApps {
		return DockLayout{}, fmt.Errorf("dock: %d pinned applications exceed %d", len(layout.Pinned), maxPinnedApps)
	}
	layout.Pinned = dedupe(layout.Pinned)
	return layout, nil
}

// ParseLauncherLayout reads and validates one stored launcher layout.
func ParseLauncherLayout(value string) (LauncherLayout, error) {
	if strings.TrimSpace(value) == "" {
		return DefaultLauncherLayout(), nil
	}
	layout := LauncherLayout{}
	if err := strictDecode(value, &layout); err != nil {
		return LauncherLayout{}, fmt.Errorf("dock: read launcher layout: %w", err)
	}
	if layout.SchemaVersion != PreferenceLayoutVersion {
		return LauncherLayout{}, fmt.Errorf("dock: launcher layout schemaVersion %d is not supported", layout.SchemaVersion)
	}
	switch layout.Sort {
	case LauncherSortName, LauncherSortRecentlyUsed:
	default:
		return LauncherLayout{}, fmt.Errorf("dock: launcher sort %q is not name or recent", layout.Sort)
	}
	if len(layout.Categories) > maxCategories {
		return LauncherLayout{}, fmt.Errorf("dock: %d categories exceed %d", len(layout.Categories), maxCategories)
	}
	layout.Categories = dedupe(layout.Categories)
	return layout, nil
}

// DecodeLauncherLayout reads one stored launcher layout; an unreadable or
// unsupported value reads as the default.
func DecodeLauncherLayout(value string) LauncherLayout {
	layout, err := ParseLauncherLayout(value)
	if err != nil {
		return DefaultLauncherLayout()
	}
	return layout
}

// PinnedOrder orders application identities the way the dock layout pins them.
// An identity the layout does not pin keeps its place after the pinned ones, so
// a new install is visible without editing the layout.
func PinnedOrder(pinned []string, apps []string) []string {
	rank := make(map[string]int, len(pinned))
	for index, name := range pinned {
		if _, seen := rank[name]; !seen {
			rank[name] = index
		}
	}
	ordered := append([]string(nil), apps...)
	sort.SliceStable(ordered, func(left, right int) bool {
		leftRank, leftPinned := rank[ordered[left]]
		rightRank, rightPinned := rank[ordered[right]]
		switch {
		case leftPinned && rightPinned:
			return leftRank < rightRank
		case leftPinned:
			return true
		case rightPinned:
			return false
		default:
			return ordered[left] < ordered[right]
		}
	})
	return ordered
}

// strictDecode reads one JSON value, refusing fields this product does not
// declare (DASH-C-27: an unknown key is ignored at the store, never silently
// honoured here).
func strictDecode(value string, target any) error {
	decoder := json.NewDecoder(bytes.NewReader([]byte(value)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// dedupe removes repeated identities and empty entries, keeping the first
// occurrence's order.
func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	kept := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		kept = append(kept, trimmed)
	}
	return kept
}
