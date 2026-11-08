package wm

import (
	"encoding/json"
	"fmt"
)

// Size is the default window size preference (DASH-C-27): the size a new window
// takes in every browser of one account.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// DefaultSize is the size a window takes before an account stores a preference.
var DefaultSize = Size{Width: 1280, Height: 720}

// Encode renders the size as the preference value the control plane stores.
func (s Size) Encode() (string, error) {
	return encode(s)
}

// DecodeSize reads a stored default size preference. An empty value is the
// absence of a preference, not a size of zero.
func DecodeSize(value string) (Size, error) {
	var size Size
	if err := decode(value, &size); err != nil {
		return Size{}, fmt.Errorf("decode default size: %w", err)
	}
	if size.Width <= 0 || size.Height <= 0 {
		return Size{}, fmt.Errorf("decode default size: %dx%d is not a window size", size.Width, size.Height)
	}
	return size, nil
}

// Placement is where one window sat (DASH-C-27): the account keeps the placement
// of its windows, so another browser of the same account lays them out the same
// way.
type Placement struct {
	Application string `json:"application"`
	Instance    int    `json:"instance"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	IsMinimized bool   `json:"minimized,omitempty"`
}

// Layout is the session snapshot the migrated codec renders. Records is the
// seam a layout store persists (DASH-C-27): this value carries minimization,
// which belongs to the browser session only and never to the layout domain.
type Layout struct {
	Windows []Placement `json:"windows"`
}

// Encode renders the layout as one JSON value.
func (l Layout) Encode() (string, error) {
	if l.Windows == nil {
		l.Windows = []Placement{}
	}
	return encode(l)
}

// DecodeLayout reads one stored layout value.
func DecodeLayout(value string) (Layout, error) {
	var layout Layout
	if err := decode(value, &layout); err != nil {
		return Layout{}, fmt.Errorf("decode layout: %w", err)
	}
	return layout, nil
}

// encode renders one value as one JSON string.
func encode(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode preference: %w", err)
	}
	return string(encoded), nil
}

// decode reads one JSON value.
func decode(value string, into any) error {
	if err := json.Unmarshal([]byte(value), into); err != nil {
		return fmt.Errorf("read preference: %w", err)
	}
	return nil
}
