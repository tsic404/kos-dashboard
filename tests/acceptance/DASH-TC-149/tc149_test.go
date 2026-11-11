// Package dashtc149 verifies DASH-TC-149: a write above the record or
// serialized limit is refused with a reason and leaves the stored value
// untouched; a migratable legacy value is migrated and stays readable; a value
// of an unsupported higher version reads as the default layout without
// overwriting storage, and an older client's write against it is refused with
// an upgrade prompt (DASH-AC-70, DASH-C-27).
package dashtc149

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/pkg/identity"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/dock"
	"github.com/tsic404/kos-dashboard/pkg/wm"
	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

const (
	ownerName     = "owner"
	ownerPassword = "owner-password"
	// declaredLayoutBytes is the serialized limit DASH-C-27 declares for one
	// window-layout value: 64 KiB, that is 65536 bytes. It stays a literal here
	// so the acceptance bar cannot move with the implementation's own constant.
	declaredLayoutBytes = 65536
)

// layoutPath is the control-plane preference key DASH-C-27 fixes.
func layoutPath() string {
	return "/api/v1/preferences/" + dock.Identifier + "/" + dock.PreferenceWindowLayout
}

// preferencePath reads the account's whole preference domain.
func preferencePath() string {
	return "/api/v1/preferences/" + dock.Identifier
}

// startControlPlane boots the real control plane with the desktop package
// installed, and a browser signed in through the front.
func startControlPlane(t *testing.T) (*harness.Shell, string, *front.Front) {
	t.Helper()
	cluster := harness.Start(t)
	cluster.InstallCRD(t, dock.APIGroup, dock.EntryVersion, dock.EntryKind, dock.EntryResource, true)
	installation := cluster.InstallPackage(t, "dashboard", dock.Declaration(), extension.MemoryArtifacts{})
	shell := cluster.StartShell(t)
	shell.Use(installation)
	owner := shell.SignInOwner(t, ownerName, ownerPassword)
	upstream, err := url.Parse(shell.Accounts.HTTP.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	desktop := front.Start(t, upstream, front.Contribution(t,
		front.Manifest("media", "multi", "normal", 800, 600, "/media")))
	return shell, owner, desktop
}

// stored reads the raw stored layout value, the way any later read will see it.
func stored(t *testing.T, shell *harness.Shell, owner string) string {
	t.Helper()
	status, answer := shell.Call(t, http.MethodGet, preferencePath(), nil, owner)
	if status != http.StatusOK {
		t.Fatalf("read the preference domain = %d %v", status, answer)
	}
	items, _ := answer["items"].(map[string]any)
	value, _ := items[dock.PreferenceWindowLayout].(string)
	return value
}

// write sends one layout write through the front and answers it.
func write(t *testing.T, desktop *front.Front, value string) front.Answer {
	t.Helper()
	return desktop.CallJSON(http.MethodPut, layoutPath(), map[string]any{"value": value})
}

// layoutRecords renders n distinct records inside one value.
func layoutRecords(t *testing.T, count int) string {
	t.Helper()
	records := make([]wm.Record, count)
	for index := range records {
		records[index] = wm.Record{
			Application: "app", Instance: index + 1,
			X: 0, Y: 0, Width: 100, Height: 100,
			State: wm.StateNormal, Workspace: wm.WorkspaceDefault,
		}
	}
	encoded, err := wm.WindowLayout{Records: records}.Encode()
	if err != nil {
		t.Fatalf("encode %d records: %v", count, err)
	}
	return encoded
}

// layoutWithSerializedSize renders an ordinary layout whose encoded value is
// exactly size bytes: the records carry long but valid identifiers and the last
// workspace absorbs the remainder.
func layoutWithSerializedSize(t *testing.T, size int) string {
	t.Helper()
	records := make([]wm.Record, wm.MaxWindowLayoutRecords)
	for index := range records {
		records[index] = wm.Record{
			Application: "com.example.media.application.media-browser",
			Instance:    index + 1,
			X:           0, Y: 0, Width: 100, Height: 100,
			State:     wm.StateNormal,
			Workspace: "workspace-" + strings.Repeat("w", 32),
			Display:   "display-" + strings.Repeat("d", 28),
		}
	}
	encoded, err := wm.WindowLayout{Records: records}.Encode()
	if err != nil {
		t.Fatalf("encode %d long-identifier records: %v", len(records), err)
	}
	pad := size - len(encoded)
	if pad < 0 {
		t.Fatalf("size = %d, below the %d bytes these records encode", size, len(encoded))
	}
	records[len(records)-1].Workspace += strings.Repeat("w", pad)
	encoded, err = wm.WindowLayout{Records: records}.Encode()
	if err != nil {
		t.Fatalf("encode the padded layout: %v", err)
	}
	if len(encoded) != size {
		t.Fatalf("encoded layout = %d bytes, want exactly %d", len(encoded), size)
	}
	return encoded
}

// TestTC149LimitsMigrationAndVersions is the case DASH-TC-149 describes.
func TestTC149LimitsMigrationAndVersions(t *testing.T) {
	shell, owner, desktop := startControlPlane(t)

	// An account that stored no layout reads an empty collection rather than
	// null, so a browser can iterate the records without a guard (DASH-C-27).
	fresh := desktop.Call(http.MethodGet, "/api/v1/desktop/layout", nil)
	if fresh.Status != http.StatusOK {
		t.Fatalf("read a fresh layout = %d %v", fresh.Status, fresh.Body)
	}
	if !strings.Contains(fresh.Body, `"records":[]`) || !strings.Contains(fresh.Body, `"isStored":false`) {
		t.Fatalf("fresh layout = %s, want an empty records collection", fresh.Body)
	}

	// A first write stores the layout the account keeps as its baseline.
	baseline := layoutRecords(t, 2)
	if answer := write(t, desktop, baseline); answer.Status != http.StatusNoContent {
		t.Fatalf("write the baseline = %d %v", answer.Status, answer.Body)
	}
	if got := stored(t, shell, owner); got != baseline {
		t.Fatalf("stored baseline = %q, want what was written", got)
	}

	// A layout of exactly the declared 64 KiB serialized limit is stored whole
	// and read back unchanged, and one byte more is refused with that limit
	// named. The bar is the literal DASH-C-27 declares, not the constant the
	// implementation happens to carry (DASH-C-27).
	atLimit := layoutWithSerializedSize(t, declaredLayoutBytes)
	if answer := write(t, desktop, atLimit); answer.Status != http.StatusNoContent {
		t.Fatalf("write a %d-byte layout = %d %v, want %d",
			len(atLimit), answer.Status, answer.Body, http.StatusNoContent)
	}
	if got := stored(t, shell, owner); got != atLimit {
		t.Fatalf("stored layout = %d bytes, want the %d written", len(got), len(atLimit))
	}
	overLimit := layoutWithSerializedSize(t, declaredLayoutBytes+1)
	overLimitAnswer := write(t, desktop, overLimit)
	if overLimitAnswer.Status != http.StatusBadRequest {
		t.Fatalf("write a %d-byte layout = %d %v, want %d",
			len(overLimit), overLimitAnswer.Status, overLimitAnswer.Body, http.StatusBadRequest)
	}
	if reason := failureMessage(overLimitAnswer); !strings.Contains(reason, "65536") {
		t.Fatalf("serialized limit refusal = %q, want the limit named", reason)
	}
	if got := stored(t, shell, owner); got != atLimit {
		t.Fatalf("stored layout after an over-limit write = %d bytes, want the %d kept", len(got), len(atLimit))
	}
	if answer := write(t, desktop, baseline); answer.Status != http.StatusNoContent {
		t.Fatalf("restore the baseline = %d %v", answer.Status, answer.Body)
	}

	// A write above the record limit is refused with a reason and leaves the
	// stored value alone (DASH-C-27).
	tooMany := write(t, desktop, layoutRecords(t, wm.MaxWindowLayoutRecords+1))
	if tooMany.Status != http.StatusBadRequest {
		t.Fatalf("write %d records = %d %v, want %d",
			wm.MaxWindowLayoutRecords+1, tooMany.Status, tooMany.Body, http.StatusBadRequest)
	}
	reason := failureMessage(tooMany)
	if !strings.Contains(reason, "50") {
		t.Fatalf("record limit refusal = %q, want the limit named", reason)
	}
	if got := stored(t, shell, owner); got != baseline {
		t.Fatalf("stored value after an over-limit write = %q, want the original", got)
	}

	// A coordinate beyond any real desktop is refused with a reason instead of
	// being stored for a browser to clamp into view (DASH-C-27).
	absurd := `{"schemaVersion":2,"records":[{"application":"app","instance":1,` +
		`"x":9223372036854775807,"y":0,"width":100,"height":100,` +
		`"state":"normal","workspace":"main"}]}`
	outOfRange := write(t, desktop, absurd)
	if outOfRange.Status != http.StatusBadRequest {
		t.Fatalf("write an out-of-range coordinate = %d %v, want %d", outOfRange.Status, outOfRange.Body, http.StatusBadRequest)
	}
	if reason := failureMessage(outOfRange); !strings.Contains(reason, "允许范围") {
		t.Fatalf("coordinate refusal = %q, want the allowed range named", reason)
	}
	if got := stored(t, shell, owner); got != baseline {
		t.Fatalf("stored value after an out-of-range write = %q, want the original", got)
	}

	// A write that repeats the accepted request changes nothing: one key, one
	// value (KOSD-C-12 idempotency).
	if answer := write(t, desktop, baseline); answer.Status != http.StatusNoContent {
		t.Fatalf("repeat the baseline write = %d %v", answer.Status, answer.Body)
	}
	preferences := storedPreferences(t, shell, ownerName)
	if len(preferences) != 1 || preferences[0].Key != dock.PreferenceWindowLayout || preferences[0].Value != baseline {
		t.Fatalf("stored preferences after a repeated write = %+v, want one row", preferences)
	}

	// A stored value above the record limit is read whole: a read never
	// truncates the collection it was handed (DASH-C-27).
	seeded := layoutRecords(t, wm.MaxWindowLayoutRecords+1)
	if status, answer := shell.Call(t, http.MethodPut, layoutPath(), map[string]any{"value": seeded}, owner); status != http.StatusNoContent {
		t.Fatalf("seed an above-limit value = %d %v", status, answer)
	}
	whole := desktop.Call(http.MethodGet, "/api/v1/desktop/layout", nil)
	if whole.Status != http.StatusOK {
		t.Fatalf("read the above-limit value = %d %v", whole.Status, whole.Body)
	}
	if count := len(recordsOf(t, whole)); count != wm.MaxWindowLayoutRecords+1 {
		t.Fatalf("above-limit read = %d records, want the %d stored", count, wm.MaxWindowLayoutRecords+1)
	}
	if answer := write(t, desktop, baseline); answer.Status != http.StatusNoContent {
		t.Fatalf("restore the baseline = %d %v", answer.Status, answer.Body)
	}

	// An older client had written a migratable legacy value; this product reads
	// it as the current version with the layout visible and leaves the stored
	// encoding alone (DASH-C-27).
	legacy := `{"windows":[{"application":"media","instance":1,"x":40,"y":24,` +
		`"width":800,"height":600,"minimized":true}]}`
	if status, answer := shell.Call(t, http.MethodPut, layoutPath(), map[string]any{"value": legacy}, owner); status != http.StatusNoContent {
		t.Fatalf("seed the legacy value = %d %v", status, answer)
	}
	migrated := desktop.Call(http.MethodGet, "/api/v1/desktop/layout", nil)
	if migrated.Status != http.StatusOK {
		t.Fatalf("read the migrated layout = %d %v", migrated.Status, migrated.Body)
	}
	if version := number(migrated, "schemaVersion"); version != wm.WindowLayoutVersion {
		t.Fatalf("migrated schemaVersion = %d, want %d", version, wm.WindowLayoutVersion)
	}
	migratedRecords := recordsOf(t, migrated)
	if len(migratedRecords) != 1 || migratedRecords[0].Application != "media" ||
		migratedRecords[0].Width != 800 || migratedRecords[0].State != wm.StateNormal {
		t.Fatalf("migrated records = %+v, want the placement with minimization dropped", migratedRecords)
	}
	if got := stored(t, shell, owner); got != legacy {
		t.Fatalf("stored value after a migrating read = %q, want the legacy encoding untouched", got)
	}

	// A stored version below this product's own is unsupported too, and its
	// reason must not claim the stored version is the higher one (DASH-C-27).
	if status, answer := shell.Call(t, http.MethodPut, layoutPath(), map[string]any{"value": `{"schemaVersion":0,"records":[]}`}, owner); status != http.StatusNoContent {
		t.Fatalf("seed a zero-version value = %d %v", status, answer)
	}
	lower := desktop.Call(http.MethodGet, "/api/v1/desktop/layout", nil)
	if lower.Status != http.StatusOK {
		t.Fatalf("read the zero-version layout = %d %v", lower.Status, lower.Body)
	}
	if supported, _ := lower.Decoded["isSupported"].(bool); supported {
		t.Fatalf("zero-version read = %v, want it reported as unsupported", lower.Body)
	}
	lowerReason, _ := lower.Decoded["reason"].(string)
	if strings.Contains(lowerReason, "高于") || !strings.Contains(lowerReason, "不受当前产品支持") {
		t.Fatalf("zero-version reason = %q, want it to say the version is unsupported", lowerReason)
	}

	// A newer client writes a higher version directly through the control
	// plane (as that client would). This product reads the default layout and
	// leaves the stored value alone (DASH-C-27).
	higher := `{"schemaVersion":3,"records":[]}`
	if status, answer := shell.Call(t, http.MethodPut, layoutPath(), map[string]any{"value": higher}, owner); status != http.StatusNoContent {
		t.Fatalf("write a newer version through the control plane = %d %v", status, answer)
	}
	read := desktop.Call(http.MethodGet, "/api/v1/desktop/layout", nil)
	if read.Status != http.StatusOK {
		t.Fatalf("read the higher version = %d %v", read.Status, read.Body)
	}
	if len(recordsOf(t, read)) != 0 {
		t.Fatalf("higher version read = %v, want the default layout", read.Body)
	}
	if supported, _ := read.Decoded["isSupported"].(bool); supported {
		t.Fatalf("higher version read = %v, want it reported as unsupported", read.Body)
	}
	if got := stored(t, shell, owner); got != higher {
		t.Fatalf("stored value after reading a higher version = %q, want it untouched", got)
	}

	// An older client's write against the newer stored value is refused with an
	// upgrade prompt, and the stored value stays (DASH-C-27).
	refused := write(t, desktop, baseline)
	if refused.Status != http.StatusConflict {
		t.Fatalf("old-client write over a higher version = %d %v, want %d",
			refused.Status, refused.Body, http.StatusConflict)
	}
	if reason := failureMessage(refused); !strings.Contains(reason, "升级") {
		t.Fatalf("upgrade refusal = %q, want the upgrade prompt", reason)
	}
	if got := stored(t, shell, owner); got != higher {
		t.Fatalf("stored value after the refused write = %q, want it untouched", got)
	}
}

// failureMessage reads one refusal's message.
func failureMessage(answer front.Answer) string {
	failure, _ := answer.Decoded["error"].(map[string]any)
	message, _ := failure["message"].(string)
	return message
}

// storedPreferences reads the account's preference rows from the control-plane
// database, the source the store writes land in (KOSD-C-27).
func storedPreferences(t *testing.T, shell *harness.Shell, username string) []identity.Preference {
	t.Helper()
	rows, err := shell.Accounts.Store.Preferences(t.Context(), username)
	if err != nil {
		t.Fatalf("read stored preferences: %v", err)
	}
	kept := make([]identity.Preference, 0, len(rows))
	for _, row := range rows {
		if row.Extension == dock.Identifier {
			kept = append(kept, row)
		}
	}
	return kept
}

// number reads one JSON number out of an answer.
func number(answer front.Answer, field string) int {
	value, _ := answer.Decoded[field].(float64)
	return int(value)
}

// recordsOf decodes the records of one layout answer.
func recordsOf(t *testing.T, answer front.Answer) []wm.Record {
	t.Helper()
	raw, err := json.Marshal(answer.Decoded["records"])
	if err != nil {
		t.Fatalf("encode records: %v", err)
	}
	var records []wm.Record
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("decode records: %v (%s)", err, fmt.Sprint(answer.Decoded["records"]))
	}
	return records
}
