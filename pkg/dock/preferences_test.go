package dock_test

import (
	"strings"
	"testing"

	"github.com/tsic404/kos-dashboard/pkg/dock"
)

func TestDockLayoutIsReadBack(t *testing.T) {
	// A stored value is a JSON document; the reader normalizes it, so an account
	// that pinned the same application twice sees it once (DASH-C-27).
	decoded, err := dock.ParseDockLayout(
		`{"schemaVersion":1,"position":"left","autoHide":true,"pinned":["files","files","notes"]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if decoded.Position != dock.DockPositionLeft || !decoded.AutoHide {
		t.Fatalf("decoded = %+v, want the stored layout", decoded)
	}
	if len(decoded.Pinned) != 2 || decoded.Pinned[0] != "files" || decoded.Pinned[1] != "notes" {
		t.Fatalf("pinned = %v, want the duplicates removed in order", decoded.Pinned)
	}
}

func TestDockLayoutRefusals(t *testing.T) {
	cases := map[string]string{
		"unknown field": `{"schemaVersion":1,"position":"bottom","autoHide":false,"pinned":[],"extra":true}`,
		"bad position":  `{"schemaVersion":1,"position":"top","autoHide":false,"pinned":[]}`,
		"bad version":   `{"schemaVersion":9,"position":"bottom","autoHide":false,"pinned":[]}`,
	}
	for name, value := range cases {
		if _, err := dock.ParseDockLayout(value); err == nil {
			t.Fatalf("%s: layout %s parsed, want a refusal", name, value)
		}
	}
	// Reading never fails: an unreadable value answers the default and stays
	// stored until the account writes again (DASH-C-27).
	if got := dock.DecodeDockLayout("not json"); got.Position != dock.DockPositionBottom {
		t.Fatalf("decoded default = %+v, want the bottom dock", got)
	}
}

func TestLauncherLayoutRefusals(t *testing.T) {
	if _, err := dock.ParseLauncherLayout(`{"schemaVersion":1,"sort":"chaos","categories":[]}`); err == nil {
		t.Fatal("unknown sort order parsed, want a refusal")
	}
	if got := dock.DecodeLauncherLayout(`{"schemaVersion":1,"sort":"recent","categories":["work"]}`); got.Sort != dock.LauncherSortRecentlyUsed {
		t.Fatalf("decoded = %+v, want the recent order", got)
	}
	if got := dock.DecodeLauncherLayout(""); got.Sort != dock.LauncherSortName {
		t.Fatalf("decoded empty = %+v, want the default order", got)
	}
}

func TestPinnedOrderKeepsUnpinnedApplicationsVisible(t *testing.T) {
	ordered := dock.PinnedOrder([]string{"notes", "files"}, []string{"apps", "files", "notes"})
	if strings.Join(ordered, ",") != "notes,files,apps" {
		t.Fatalf("order = %v, want the pinned ones first and the rest by name", ordered)
	}
	// A pin naming an application that is not installed keeps its place and
	// surfaces nothing (DASH-C-30).
	if got := dock.PinnedOrder([]string{"absent"}, []string{"files"}); strings.Join(got, ",") != "files" {
		t.Fatalf("order = %v, want only the installed application", got)
	}
}
