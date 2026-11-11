// Package dashtc150 verifies DASH-TC-150: window interaction never enters the
// cluster objects or the preference value beyond layout records — no open set,
// z-order, focus or minimization — and once the control-plane database stops,
// the layout is unreadable and unwritable while applications keep reconciling
// (DASH-AC-70, DASH-C-27, DASH-C-74, KOSD-C-61, KOSD-F-19).
package dashtc150

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tsic404/kosd/pkg/app"
	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/dock"
	"github.com/tsic404/kos-dashboard/pkg/wm"
	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

const (
	applicationName = "media"
	definitionName  = "media-template"
	ownerName       = "owner"
	ownerPassword   = "owner-password"
)

// mediaTemplate renders the workload the application owns, so the case has a
// downstream workload the window interaction must leave alone.
const mediaTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Name }}
  namespace: {{ .Namespace }}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: {{ .Name }}
  template:
    metadata:
      labels:
        app: {{ .Name }}
    spec:
      containers:
        - name: media
          image: example.test/media:1
`

// layoutPath is where the shell writes the account's layout: the control
// plane's preference API under the key DASH-C-27 names.
func layoutPath() string {
	return "/api/v1/preferences/" + dock.Identifier + "/" + dock.PreferenceWindowLayout
}

// preferencePath reads the whole preference domain.
func preferencePath() string {
	return "/api/v1/preferences/" + dock.Identifier
}

// sessionStateNames are the session-only names a layout write, an object or a
// preference value must never carry (DASH-C-27, DASH-C-74, KOSD-F-19).
var sessionStateNames = []string{
	"windows", "openSet", "openWindows", "zOrder", "z",
	"focus", "focused", "isFocused", "minimized", "isMinimized", "isUnsaved",
}

// allowedRecordFields are the only fields one layout record carries.
var allowedRecordFields = map[string]bool{
	"application": true, "instance": true,
	"x": true, "y": true, "width": true, "height": true,
	"state": true, "workspace": true, "display": true, "pinned": true,
}

// startWorld boots the cluster, the application with its workload, the control
// plane with the desktop package installed and one browser signed in.
func startWorld(t *testing.T) (*harness.Cluster, *harness.Shell, string, *front.Front) {
	t.Helper()
	cluster := harness.Start(t)
	cluster.InstallDefinition(t, harness.DefinitionWithTemplate(t, definitionName, mediaTemplate, nil))
	cluster.CreateApplication(t, applicationName, definitionName, harness.Namespace)
	controller := cluster.ApplicationController(t, app.Options{})
	if phase := cluster.SettleApplication(t, controller, applicationName, 6); phase != app.PhaseReady {
		t.Fatalf("application phase = %q, want %q", phase, app.PhaseReady)
	}
	cluster.InstallCRD(t, dock.APIGroup, dock.EntryVersion, dock.EntryKind, dock.EntryResource, true)
	installation := cluster.InstallPackage(t, "dashboard", dock.Declaration(), extension.MemoryArtifacts{})
	shell := cluster.StartShell(t)
	shell.Use(installation)
	owner := shell.SignInOwner(t, ownerName, ownerPassword)
	upstream, err := url.Parse(shell.Accounts.HTTP.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	desktop := front.Start(t, upstream,
		front.Contribution(t, front.Manifest(applicationName, "multi", "normal", 800, 600, "/media")))
	return cluster, shell, owner, desktop
}

// open opens one window and answers it.
func open(t *testing.T, desktop *front.Front, body map[string]any) map[string]any {
	t.Helper()
	answer := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows", body)
	if answer.Status != http.StatusAccepted {
		t.Fatalf("open window %v = %d %v", body, answer.Status, answer.Body)
	}
	return front.Window(t, answer)
}

// setWindow applies one window-session write.
func setWindow(t *testing.T, desktop *front.Front, window, suffix string, body map[string]any) front.Answer {
	t.Helper()
	answer := desktop.CallJSON(http.MethodPut, "/api/v1/desktop/windows/"+window+suffix, body)
	if answer.Status != http.StatusOK {
		t.Fatalf("write %s = %d %v", suffix, answer.Status, answer.Body)
	}
	return answer
}

// layoutOf reads the session's layout projection.
func layoutOf(t *testing.T, answer front.Answer) wm.WindowLayout {
	t.Helper()
	raw, found := answer.Decoded["layout"]
	if !found {
		t.Fatalf("session answer carries no layout: %v", answer.Decoded)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encode the session layout: %v", err)
	}
	var layout wm.WindowLayout
	if err := json.Unmarshal(encoded, &layout); err != nil {
		t.Fatalf("decode the session layout: %v", err)
	}
	return layout
}

// TestTC150PreferencesOutOfObjectsAndOutage is the case DASH-TC-150 describes.
func TestTC150PreferencesOutOfObjectsAndOutage(t *testing.T) {
	cluster, shell, owner, desktop := startWorld(t)
	before := clusterState(t, cluster)

	// Open, minimize, focus and reorder windows: every one of those is session
	// state (DASH-C-74).
	first := open(t, desktop, map[string]any{"app": applicationName, "path": "/media/a"})
	second := open(t, desktop, map[string]any{"app": applicationName, "path": "/media/b"})
	firstID, secondID := first["id"].(string), second["id"].(string)
	setWindow(t, desktop, firstID, "/bounds", map[string]any{"x": 40, "y": 24, "width": 640, "height": 480})
	setWindow(t, desktop, firstID, "/state", map[string]any{"state": "minimized"})
	setWindow(t, desktop, secondID, "/bounds", map[string]any{"x": 120, "y": 80, "width": 720, "height": 520})
	if focused := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows/"+secondID+"/focus", nil); focused.Status != http.StatusOK {
		t.Fatalf("focus the second window = %d %v", focused.Status, focused.Body)
	}
	if closed := desktop.Call(http.MethodDelete, "/api/v1/desktop/windows/"+firstID, nil); closed.Status != http.StatusOK {
		t.Fatalf("close the first window = %d %v", closed.Status, closed.Body)
	}

	// The session carries the interaction state; the layout projection it
	// hands the shell carries records only (DASH-C-27, DASH-C-74).
	session := desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)
	if session.Status != http.StatusOK {
		t.Fatalf("read the session = %d %v", session.Status, session.Body)
	}
	windows := front.Windows(session)
	if len(windows) != 1 || windows[0]["z"] == nil || windows[0]["isFocused"] != true {
		t.Fatalf("session windows = %v, want the focused window with its z-order", windows)
	}
	chosen := layoutOf(t, session)
	encoded, err := chosen.Encode()
	if err != nil {
		t.Fatalf("encode the layout projection: %v", err)
	}
	assertLayoutOnly(t, encoded)
	if status := desktop.CallJSON(http.MethodPut, layoutPath(), map[string]any{"value": encoded}).Status; status != http.StatusNoContent {
		t.Fatalf("write the layout = %d, want %d", status, http.StatusNoContent)
	}

	// Neither the cluster objects nor the preference domain gained anything
	// from the window interaction except the layout records (DASH-C-27,
	// KOSD-F-19).
	if current := clusterState(t, cluster); current != before {
		t.Fatalf("cluster after the window interaction = %q, want %q", current, before)
	}
	assertNoSessionState(t, "应用对象", cluster.Get(t, "v1beta1", "Application", applicationName).Object)
	status, stored := shell.Call(t, http.MethodGet, preferencePath(), nil, owner)
	if status != http.StatusOK {
		t.Fatalf("read the preference domain = %d %v", status, stored)
	}
	items, _ := stored["items"].(map[string]any)
	if len(items) != 1 {
		t.Fatalf("preference domain = %v, want the layout key alone", items)
	}
	value, _ := items[dock.PreferenceWindowLayout].(string)
	if value != encoded {
		t.Fatalf("stored layout = %q, want what the shell wrote", value)
	}
	assertLayoutOnly(t, value)

	// The control-plane database stops: the layout is no longer readable and a
	// write is refused with the control plane's own envelope (KOSD-C-61).
	if err := shell.Accounts.Store.Close(); err != nil {
		t.Fatalf("stop the control-plane database: %v", err)
	}
	read := desktop.Call(http.MethodGet, "/api/v1/desktop/layout?viewport=1440x900", nil)
	if read.Status != http.StatusServiceUnavailable || front.ErrorCode(read) != "database_unavailable" {
		t.Fatalf("layout read with the database stopped = %d %v, want 503 database_unavailable", read.Status, read.Body)
	}
	write := desktop.CallJSON(http.MethodPut, layoutPath(), map[string]any{"value": encoded})
	if write.Status != http.StatusServiceUnavailable || front.ErrorCode(write) != "database_unavailable" {
		t.Fatalf("layout write with the database stopped = %d %v, want 503 database_unavailable", write.Status, write.Body)
	}

	// The cluster and its reconciliation do not depend on that database.
	cluster.InstallDefinition(t, harness.DefinitionWithTemplate(t, "offline-db", mediaTemplate, nil))
	cluster.CreateApplication(t, "offline", "offline-db", harness.Namespace)
	offline := cluster.ApplicationController(t, app.Options{})
	if phase := cluster.SettleApplication(t, offline, "offline", 10); phase != app.PhaseReady {
		t.Fatalf("application phase with the database stopped = %q, want %q", phase, app.PhaseReady)
	}
}

// clusterState renders the application's phase and resource version and every
// workload as one string, so any write the window interaction made shows up.
func clusterState(t *testing.T, cluster *harness.Cluster) string {
	t.Helper()
	application := cluster.Get(t, "v1beta1", "Application", applicationName)
	state := harness.String(application.Object, "status", "phase") + "/" + application.GetResourceVersion()
	list, err := cluster.Kube.AppsV1().Deployments(harness.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	for index := range list.Items {
		deployment := &list.Items[index]
		replicas := int32(0)
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		state += ";" + deployment.Name + "/" + string(deployment.UID) + "/" +
			deployment.ResourceVersion + "/" + strconv.Itoa(int(replicas))
	}
	return state
}

// assertLayoutOnly checks that one stored value carries the layout collection
// and nothing else: no open set, z-order, focus or minimization.
func assertLayoutOnly(t *testing.T, value string) {
	t.Helper()
	var layout map[string]any
	if err := json.Unmarshal([]byte(value), &layout); err != nil {
		t.Fatalf("decode the stored layout %q: %v", value, err)
	}
	for key := range layout {
		if key != "schemaVersion" && key != "records" {
			t.Fatalf("layout value carries the field %q: %s", key, value)
		}
	}
	records, _ := layout["records"].([]any)
	for index, raw := range records {
		record, _ := raw.(map[string]any)
		for key := range record {
			if !allowedRecordFields[key] {
				t.Fatalf("layout record %d carries the field %q: %v", index, key, record)
			}
		}
	}
}

// assertNoSessionState walks one cluster object's document and fails on any
// session-only name (DASH-C-27, DASH-C-74).
func assertNoSessionState(t *testing.T, label string, document map[string]any) {
	t.Helper()
	var walk func(value any, path string)
	walk = func(value any, path string) {
		switch typed := value.(type) {
		case map[string]any:
			for key, nested := range typed {
				for _, name := range sessionStateNames {
					if key == name {
						t.Fatalf("%s carries the session field %q at %s", label, key, path)
					}
				}
				walk(nested, path+"."+key)
			}
		case []any:
			for index, nested := range typed {
				walk(nested, path+"["+strconv.Itoa(index)+"]")
			}
		}
	}
	walk(document, "")
}
