package wm

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// layoutRecord builds one layout record with the fields a case does not vary.
func layoutRecord(application string, instance, x, y, width, height int) Record {
	return Record{
		Application: application,
		Instance:    instance,
		X:           x,
		Y:           y,
		Width:       width,
		Height:      height,
		State:       StateNormal,
		Workspace:   WorkspaceDefault,
	}
}

// TestWindowLayoutRoundTrips checks that the layout this product encodes is the
// layout it reads back, at the current version (DASH-C-27).
func TestWindowLayoutRoundTrips(t *testing.T) {
	layout := WindowLayout{Records: []Record{
		layoutRecord("media", 1, 40, 24, 800, 600),
		{Application: "notes", Instance: 2, X: 120, Y: 60, Width: 640, Height: 480,
			State: StateMaximized, Workspace: "work", Display: "left", Pinned: true},
	}}
	encoded, err := layout.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(encoded, `"schemaVersion":2`) {
		t.Fatalf("encoded layout = %s, want the current schema version", encoded)
	}
	read := ReadWindowLayout(encoded)
	if !read.IsSupported || read.IsMigrated {
		t.Fatalf("read = %+v, want a supported current value", read)
	}
	if len(read.Layout.Records) != 2 {
		t.Fatalf("records = %+v, want both", read.Layout.Records)
	}
	second := read.Layout.Records[1]
	if second.State != StateMaximized || second.Workspace != "work" ||
		second.Display != "left" || !second.Pinned {
		t.Fatalf("second record = %+v, want the stored layout", second)
	}
}

// TestWindowLayoutEmptyIsTheDefault checks that an account that stored nothing
// reads the default layout as supported, so a fresh account is not reported as
// having an unsupported value (DASH-C-27).
func TestWindowLayoutEmptyIsTheDefault(t *testing.T) {
	for _, value := range []string{"", "   "} {
		read := ReadWindowLayout(value)
		if !read.IsSupported || read.IsMigrated || read.Reason != "" {
			t.Fatalf("read of %q = %+v, want the supported default", value, read)
		}
		if read.Layout.SchemaVersion != WindowLayoutVersion || len(read.Layout.Records) != 0 {
			t.Fatalf("default layout = %+v, want no records", read.Layout)
		}
	}
}

// TestLegacyLayoutMigrates checks that a stored legacy session snapshot is
// migrated to the current version and stays readable, and that minimization —
// session state — is dropped on the way (DASH-C-27, DASH-C-74).
func TestLegacyLayoutMigrates(t *testing.T) {
	legacy := `{"windows":[{"application":"media","instance":1,"x":40,"y":24,` +
		`"width":800,"height":600,"minimized":true}]}`
	read := ReadWindowLayout(legacy)
	if !read.IsSupported || !read.IsMigrated {
		t.Fatalf("read = %+v, want a supported migration", read)
	}
	if read.Layout.SchemaVersion != WindowLayoutVersion || len(read.Layout.Records) != 1 {
		t.Fatalf("migrated layout = %+v, want one current record", read.Layout)
	}
	record := read.Layout.Records[0]
	if record.Application != "media" || record.Instance != 1 || record.Width != 800 || record.Height != 600 {
		t.Fatalf("migrated record = %+v, want the stored placement", record)
	}
	if record.State != StateNormal || record.Workspace != WorkspaceDefault {
		t.Fatalf("migrated record = %+v, want the layout defaults", record)
	}
}

// TestUnsupportedLayoutReadsAsDefault checks the read semantics of a value this
// product cannot honour: a higher version and a failed migration both answer the
// default layout with a reason (DASH-C-27).
func TestUnsupportedLayoutReadsAsDefault(t *testing.T) {
	cases := map[string]string{
		"higher version":   `{"schemaVersion":3,"records":[{"application":"media"}]}`,
		"failed migration": `{"windows":[{"application":"","instance":0}]}`,
		"malformed legacy": `{"windows":[{"x":1}]}`,
		"unknown field":    `{"schemaVersion":2,"records":[],"zOrder":["a"]}`,
		"not json":         `{`,
	}
	for name, value := range cases {
		read := ReadWindowLayout(value)
		if read.IsSupported {
			t.Fatalf("%s: read %s was honoured, want the default", name, value)
		}
		if read.Reason == "" {
			t.Fatalf("%s: refusal carries no reason", name)
		}
		if len(read.Layout.Records) != 0 || read.Layout.SchemaVersion != WindowLayoutVersion {
			t.Fatalf("%s: layout = %+v, want the default layout", name, read.Layout)
		}
	}
}

// layoutValue renders one written value of the current version.
func layoutValue(t *testing.T, records []Record) string {
	t.Helper()
	encoded, err := WindowLayout{Records: records}.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return encoded
}

// TestWindowLayoutWriteLimits checks the write caps and the reasons they give
// (DASH-C-27).
func TestWindowLayoutWriteLimits(t *testing.T) {
	tooMany := make([]Record, MaxWindowLayoutRecords+1)
	for index := range tooMany {
		tooMany[index] = layoutRecord("app", index+1, 0, 0, 100, 100)
	}
	err := ParseWindowLayoutWrite(layoutValue(t, tooMany), "")
	var refusal *LayoutWriteError
	if !errorsAs(err, &refusal) || refusal.Kind != LayoutWriteTooMany {
		t.Fatalf("51 records = %v, want the record limit refusal", err)
	}
	if !strings.Contains(refusal.Message, "50") {
		t.Fatalf("record limit reason = %q, want the limit in it", refusal.Message)
	}

	record := layoutRecord("app", 1, 0, 0, 100, 100)
	record.Display = strings.Repeat("d", MaxWindowLayoutBytes)
	large := layoutValue(t, []Record{record})
	if len(large) <= MaxWindowLayoutBytes {
		t.Fatalf("large value = %d bytes, want it above %d", len(large), MaxWindowLayoutBytes)
	}
	err = ParseWindowLayoutWrite(large, "")
	if !errorsAs(err, &refusal) || refusal.Kind != LayoutWriteTooLarge {
		t.Fatalf("oversized value = %v, want the serialized limit refusal", err)
	}
	if !strings.Contains(refusal.Message, "65536") {
		t.Fatalf("size limit reason = %q, want the limit in it", refusal.Message)
	}
}

// errorsAs reads one typed refusal.
func errorsAs(err error, target **LayoutWriteError) bool {
	refusal, ok := err.(*LayoutWriteError)
	if ok {
		*target = refusal
	}
	return ok
}

// TestWindowLayoutWriteVersions checks the version rules of a write: a legacy
// value writes over an older store, a stored higher version refuses every
// write with an upgrade prompt, and a value of an unknown version is refused
// (DASH-C-27).
func TestWindowLayoutWriteVersions(t *testing.T) {
	current := layoutValue(t, []Record{layoutRecord("media", 1, 0, 0, 800, 600)})
	legacy := `{"windows":[{"application":"media","instance":1,"x":0,"y":0,"width":800,"height":600}]}`

	if err := ParseWindowLayoutWrite(legacy, current); err == nil {
		t.Fatal("a legacy write over a current value was accepted, want the upgrade prompt")
	} else if !strings.Contains(err.Error(), "升级") {
		t.Fatalf("legacy-over-current refusal = %q, want the upgrade prompt", err)
	}
	if err := ParseWindowLayoutWrite(current, legacy); err != nil {
		t.Fatalf("current over legacy = %v, want the upgrade accepted", err)
	}
	if err := ParseWindowLayoutWrite(legacy, legacy); err != nil {
		t.Fatalf("legacy over legacy = %v, want an old client to keep writing", err)
	}

	higher := `{"schemaVersion":3,"records":[]}`
	err := ParseWindowLayoutWrite(current, higher)
	var refusal *LayoutWriteError
	if !errorsAs(err, &refusal) || refusal.Kind != LayoutWriteStoredNewer {
		t.Fatalf("current write over a stored newer value = %v, want the upgrade refusal", err)
	}
	if !strings.Contains(refusal.Message, "升级") {
		t.Fatalf("stored-newer reason = %q, want the upgrade prompt", refusal.Message)
	}
	if err := ParseWindowLayoutWrite(higher, ""); err == nil {
		t.Fatal("a value of a version this product cannot judge was accepted")
	}

	// An unreadable stored value is no barrier: a write repairs it.
	if err := ParseWindowLayoutWrite(current, "not json"); err != nil {
		t.Fatalf("write over an unreadable value = %v, want it accepted", err)
	}
}

// TestWindowLayoutRecordsKeepSessionStateOut checks that a record carrying
// session state or an unknown field is refused, so the open set, z-order, focus
// and minimization can never enter the preference value (DASH-C-74, KOSD-F-19).
func TestWindowLayoutRecordsKeepSessionStateOut(t *testing.T) {
	cases := map[string]string{
		"minimized":  `{"schemaVersion":2,"records":[{"application":"media","instance":1,"x":0,"y":0,"width":800,"height":600,"state":"minimized","workspace":"main"}]}`,
		"opening":    `{"schemaVersion":2,"records":[{"application":"media","instance":1,"x":0,"y":0,"width":800,"height":600,"state":"opening","workspace":"main"}]}`,
		"z-order":    `{"schemaVersion":2,"records":[{"application":"media","instance":1,"x":0,"y":0,"width":800,"height":600,"state":"normal","workspace":"main","z":3}]}`,
		"focus":      `{"schemaVersion":2,"records":[{"application":"media","instance":1,"x":0,"y":0,"width":800,"height":600,"state":"normal","workspace":"main","isFocused":true}]}`,
		"open set":   `{"schemaVersion":2,"records":[],"openSet":["media#1"]}`,
		"bad record": `{"schemaVersion":2,"records":[{"application":"media","instance":0,"x":0,"y":0,"width":800,"height":600,"state":"normal","workspace":"main"}]}`,
	}
	for name, value := range cases {
		if err := ParseWindowLayoutWrite(value, ""); err == nil {
			t.Fatalf("%s: write %s was accepted, want a refusal", name, value)
		}
	}
}

// TestAdaptWindowLayoutClampsAndFallsBack checks the read adaptation: bounds are
// clamped into the viewport, a record on a display the browser no longer has
// falls back to the primary one, and the stored value the caller passed in is
// not modified (DASH-C-27).
func TestAdaptWindowLayoutClampsAndFallsBack(t *testing.T) {
	stored := WindowLayout{Records: []Record{
		layoutRecord("media", 1, 40, 24, 800, 600),
		{Application: "notes", Instance: 2, X: 500, Y: 100, Width: 700, Height: 500,
			State: StateMaximized, Workspace: "work", Display: "left"},
	}}

	same := AdaptWindowLayout(stored, &Size{Width: 1440, Height: 900}, []string{"left"})
	if same.Records[0].X != 40 || same.Records[1].Display != "left" {
		t.Fatalf("adapted = %+v, want the stored layout inside a large viewport", same.Records)
	}

	clamped := AdaptWindowLayout(stored, &Size{Width: 800, Height: 600}, []string{"left"})
	if got := clamped.Records[0]; got.X != 0 || got.Y != 0 || got.Width != 800 || got.Height != 600 {
		t.Fatalf("first clamped record = %+v, want it fitted into the viewport", got)
	}
	if got := clamped.Records[1]; got.X != 100 || got.Y != 100 || got.Width != 700 || got.Height != 500 {
		t.Fatalf("second clamped record = %+v, want it fitted into the viewport", got)
	}

	fallen := AdaptWindowLayout(stored, &Size{Width: 800, Height: 600}, []string{})
	if fallen.Records[1].Display != "" {
		t.Fatalf("record = %+v, want the removed display to fall back to the primary one", fallen.Records[1])
	}

	// The caller's value is untouched: a read never writes the adaptation back.
	if stored.Records[1].Display != "left" || stored.Records[0].X != 40 {
		t.Fatalf("stored layout = %+v, want the read to leave it alone", stored.Records)
	}
}

// TestClampKeepsAnExtremeCoordinateInTheViewport checks the read clamp against
// a coordinate near the integer limit: the comparison must not overflow, so an
// off-screen record lands inside the viewport rather than being returned as
// stored (DASH-C-27).
func TestClampKeepsAnExtremeCoordinateInTheViewport(t *testing.T) {
	stored := WindowLayout{Records: []Record{{
		Application: "media", Instance: 1,
		X: math.MaxInt, Y: math.MaxInt, Width: 100, Height: 50,
		State: StateNormal, Workspace: WorkspaceDefault,
	}}}
	adapted := AdaptWindowLayout(stored, &Size{Width: 800, Height: 600}, nil)
	if got := adapted.Records[0]; got.X != 700 || got.Y != 550 {
		t.Fatalf("clamped record = %+v, want x=700 y=550", got)
	}
}

// TestWriteRefusesAnOutOfRangeCoordinate checks that a coordinate beyond any
// real desktop is refused when written, with a reason naming the range, so a
// stored value cannot carry one (DASH-C-27).
func TestWriteRefusesAnOutOfRangeCoordinate(t *testing.T) {
	for name, position := range map[string]string{
		"x above the range": `"x":9223372036854775807`,
		"y below the range": `"y":-9223372036854775808`,
	} {
		value := `{"schemaVersion":2,"records":[{"application":"media","instance":1,` +
			`"x":0,"y":0,"width":100,"height":50,"state":"normal","workspace":"main",` + position + `}]}`
		err := ParseWindowLayoutWrite(value, "")
		var refusal *LayoutWriteError
		if !errorsAs(err, &refusal) {
			t.Fatalf("%s: write = %v, want a refusal", name, err)
		}
		if !strings.Contains(refusal.Message, "允许范围") {
			t.Fatalf("%s: reason = %q, want the allowed range named", name, refusal.Message)
		}
	}

	// A coordinate right at the bound is still a layout this product stores.
	atBound := WindowLayout{Records: []Record{layoutRecord("media", 1, MaxWindowLayoutCoordinate, -MaxWindowLayoutCoordinate, 100, 100)}}
	atBound.Records[0].Instance = 1
	if err := ParseWindowLayoutWrite(layoutValue(t, atBound.Records), ""); err != nil {
		t.Fatalf("write at the bound = %v, want it accepted", err)
	}
}

// TestEmptyLayoutKeepsAnEmptyCollection checks that an account with no records
// reads an empty collection rather than null, and that the default layout
// carries one too (DASH-C-27).
func TestEmptyLayoutKeepsAnEmptyCollection(t *testing.T) {
	for name, layout := range map[string]WindowLayout{
		"adapted default": AdaptWindowLayout(DefaultWindowLayout(), &Size{Width: 800, Height: 600}, nil),
		"empty stored":    ReadWindowLayout("").Layout,
		"stored version":  ReadWindowLayout(`{"schemaVersion":2,"records":[]}`).Layout,
	} {
		if layout.Records == nil {
			t.Fatalf("%s: records = nil, want an empty collection", name)
		}
		payload, err := json.Marshal(layout)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if !strings.Contains(string(payload), `"records":[]`) {
			t.Fatalf("%s: JSON = %s, want records to be an empty array", name, payload)
		}
	}
}

// TestUnsupportedLayoutReasonPointsTheRightWay checks the reason of a stored
// version this product cannot honour: one above the current version says so,
// and one below or unreadable does not claim to be higher (DASH-C-27).
func TestUnsupportedLayoutReasonPointsTheRightWay(t *testing.T) {
	higher := ReadWindowLayout(`{"schemaVersion":3,"records":[]}`)
	if higher.IsSupported || !strings.Contains(higher.Reason, "高于") {
		t.Fatalf("higher version read = %+v, want a reason saying it is higher", higher)
	}
	for name, value := range map[string]string{
		"zero version":     `{"schemaVersion":0,"records":[]}`,
		"negative version": `{"schemaVersion":-1,"records":[]}`,
	} {
		read := ReadWindowLayout(value)
		if read.IsSupported {
			t.Fatalf("%s: read %s was honoured, want the default", name, value)
		}
		if strings.Contains(read.Reason, "高于") {
			t.Fatalf("%s: reason = %q, want it not to claim the stored version is higher", name, read.Reason)
		}
		if !strings.Contains(read.Reason, "不受当前产品支持") {
			t.Fatalf("%s: reason = %q, want it to say the version is unsupported", name, read.Reason)
		}
	}
}

// TestRecordsCarryPinnedAndExcludeMinimization checks the layout projection of a
// session: the pinned flag is a record field, while minimization becomes the
// normal state and never reaches the layout domain (DASH-C-27, DASH-C-74).
func TestRecordsCarryPinnedAndExcludeMinimization(t *testing.T) {
	desktop, _ := newDesktop(Size{Width: 800, Height: 600})
	window := desktop.Open("media")
	if !desktop.SetPinned(window.ID, true) {
		t.Fatal("pinning a live window failed")
	}
	if !desktop.Minimize(window.ID) {
		t.Fatal("minimizing a normal window failed")
	}
	layout := desktop.WindowLayout()
	if layout.SchemaVersion != WindowLayoutVersion || len(layout.Records) != 1 {
		t.Fatalf("layout = %+v, want one current record", layout)
	}
	record := layout.Records[0]
	if !record.Pinned {
		t.Fatalf("record = %+v, want the pinned flag carried", record)
	}
	if record.State != StateNormal {
		t.Fatalf("record state = %q, want minimization kept out of the layout domain", record.State)
	}
}
