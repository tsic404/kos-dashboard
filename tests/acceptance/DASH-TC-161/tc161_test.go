// Package dashtc161 verifies DASH-TC-161: after windows are opened, closed,
// minimized, focused and reordered, the cluster objects, the control-plane
// database and the preference values carry no open set, z-order, focus or
// minimization — only layout records enter the preference domain
// (DASH-AC-70, DASH-C-27, DASH-C-74, KOSD-F-19).
package dashtc161

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/tsic404/kosd/pkg/app"
	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/dock"
	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

const (
	applicationName = "media"
	definitionName  = "media-template"
	ownerName       = "owner"
	ownerPassword   = "owner-password"
)

// mediaTemplate renders the workload the application owns.
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

// layoutPath is where the shell writes the account's layout.
func layoutPath() string {
	return "/api/v1/preferences/" + dock.Identifier + "/" + dock.PreferenceWindowLayout
}

// preferencePath reads the whole preference domain.
func preferencePath() string {
	return "/api/v1/preferences/" + dock.Identifier
}

// sessionStateNames are the session-only names no object, no database row and
// no preference value may carry (DASH-C-27, DASH-C-74, KOSD-F-19).
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

// TestTC161SessionStateStaysOutOfPreferences is the case DASH-TC-161 describes.
func TestTC161SessionStateStaysOutOfPreferences(t *testing.T) {
	cluster, shell, owner, desktop := startWorld(t)
	before := clusterState(t, cluster)

	// Open, close, minimize, focus, reorder and switch workspaces: all of it
	// lives in the browser session (DASH-C-74).
	first := open(t, desktop, map[string]any{"app": applicationName, "path": "/media/a"})
	second := open(t, desktop, map[string]any{"app": applicationName, "path": "/media/b", "display": "left"})
	third := open(t, desktop, map[string]any{"app": applicationName, "path": "/media/c", "workspace": "work"})
	firstID, secondID, thirdID := first["id"].(string), second["id"].(string), third["id"].(string)
	setWindow(t, desktop, firstID, "/bounds", map[string]any{"x": 40, "y": 24, "width": 640, "height": 480})
	setWindow(t, desktop, secondID, "/bounds", map[string]any{"x": 120, "y": 80, "width": 720, "height": 520})
	setWindow(t, desktop, secondID, "/state", map[string]any{"state": "maximized"})
	setWindow(t, desktop, thirdID, "/pinned", map[string]any{"pinned": true})
	if focused := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows/"+secondID+"/focus", nil); focused.Status != http.StatusOK {
		t.Fatalf("focus the second window = %d %v", focused.Status, focused.Body)
	}
	if switched := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/workspaces", map[string]any{"workspace": "work"}); switched.Status != http.StatusOK {
		t.Fatalf("switch workspace = %d %v", switched.Status, switched.Body)
	}
	if switched := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/workspaces", map[string]any{"workspace": "main"}); switched.Status != http.StatusOK {
		t.Fatalf("switch back = %d %v", switched.Status, switched.Body)
	}
	if closed := desktop.Call(http.MethodDelete, "/api/v1/desktop/windows/"+secondID, nil); closed.Status != http.StatusOK {
		t.Fatalf("close the second window = %d %v", closed.Status, closed.Body)
	}
	setWindow(t, desktop, thirdID, "/state", map[string]any{"state": "minimized"})

	// The session still holds the state the interactions made.
	session := desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)
	if session.Status != http.StatusOK {
		t.Fatalf("read the session = %d %v", session.Status, session.Body)
	}
	windows := front.Windows(session)
	if len(windows) != 2 {
		t.Fatalf("session windows = %v, want the two live windows", windows)
	}
	zOrders := make(map[int]bool, len(windows))
	focused := ""
	minimized := ""
	for _, window := range windows {
		z, ok := window["z"].(float64)
		if !ok {
			t.Fatalf("window %v carries no z-order", window)
		}
		zOrders[int(z)] = true
		if window["isFocused"] == true {
			focused, _ = window["id"].(string)
		}
		if window["state"] == "minimized" {
			minimized, _ = window["id"].(string)
		}
	}
	if len(zOrders) != len(windows) {
		t.Fatalf("session z-order = %v, want distinct values", zOrders)
	}
	if minimized != thirdID {
		t.Fatalf("session minimized window = %q, want %q", minimized, thirdID)
	}
	if focused == "" {
		t.Fatalf("session focused window = %q, want exactly one focused window", focused)
	}

	// The shell persists the layout projection, and nothing else.
	raw, found := session.Decoded["layout"]
	if !found {
		t.Fatalf("session answer carries no layout: %v", session.Decoded)
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encode the session layout: %v", err)
	}
	var chosen struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(payload, &chosen); err != nil {
		t.Fatalf("decode the session layout: %v", err)
	}
	if len(chosen.Records) != 2 {
		t.Fatalf("layout records = %v, want the two open windows", chosen.Records)
	}
	for _, record := range chosen.Records {
		state, _ := record["state"].(string)
		if state == "minimized" {
			t.Fatalf("layout record = %v, want minimization kept in the session", record)
		}
	}
	encoded := string(payload)
	if status := desktop.CallJSON(http.MethodPut, layoutPath(), map[string]any{"value": encoded}).Status; status != http.StatusNoContent {
		t.Fatalf("write the layout = %d, want %d", status, http.StatusNoContent)
	}

	// One: the cluster objects kept no session state and did not move.
	if current := clusterState(t, cluster); current != before {
		t.Fatalf("cluster after the window interaction = %q, want %q", current, before)
	}
	assertNoSessionState(t, "应用对象", cluster.Get(t, "v1beta1", "Application", applicationName).Object)
	deployments, err := cluster.Kube.AppsV1().Deployments(harness.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	for index := range deployments.Items {
		deployment := deployments.Items[index]
		document, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&deployment)
		if err != nil {
			t.Fatalf("encode deployment %q: %v", deployment.Name, err)
		}
		assertNoSessionState(t, "工作负载对象", document)
	}

	// Two: the control-plane database holds the layout row and nothing with
	// session state.
	rows, err := shell.Accounts.Store.Preferences(t.Context(), ownerName)
	if err != nil {
		t.Fatalf("read the preference rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Extension != dock.Identifier || rows[0].Key != dock.PreferenceWindowLayout {
		t.Fatalf("preference rows = %+v, want the layout key alone", rows)
	}
	assertLayoutOnly(t, rows[0].Value)
	assertNoSessionStateText(t, "控制面数据库", rows[0].Value)

	// Three: the preference read answers the layout records alone.
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
		t.Fatalf("stored layout = %q, want the layout projection the session offered", value)
	}
	assertLayoutOnly(t, value)

	// A write that tries to smuggle session state into the layout is refused
	// with a reason, and the stored value stays (DASH-C-27, DASH-C-74).
	attempts := []struct {
		label string
		value string
		field string
	}{
		{
			label: "最小化字段",
			value: `{"schemaVersion":2,"records":[{"application":"media","instance":1,` +
				`"x":0,"y":0,"width":800,"height":600,"state":"normal","workspace":"main","minimized":true}]}`,
			field: "minimized",
		},
		{
			label: "z-order 字段",
			value: `{"schemaVersion":2,"records":[],"zOrder":[1,2]}`,
			field: "zOrder",
		},
		{
			label: "最小化状态",
			value: `{"schemaVersion":2,"records":[{"application":"media","instance":1,` +
				`"x":0,"y":0,"width":800,"height":600,"state":"minimized","workspace":"main"}]}`,
			field: "最小化",
		},
	}
	for _, attempt := range attempts {
		refused := desktop.CallJSON(http.MethodPut, layoutPath(), map[string]any{"value": attempt.value})
		if refused.Status != http.StatusBadRequest {
			t.Fatalf("write with %s = %d %v, want %d", attempt.label, refused.Status, refused.Body, http.StatusBadRequest)
		}
		if reason := failureMessage(refused); !strings.Contains(reason, attempt.field) {
			t.Fatalf("write with %s refused with %q, want the field named", attempt.label, reason)
		}
	}
	_, stored = shell.Call(t, http.MethodGet, preferencePath(), nil, owner)
	items, _ = stored["items"].(map[string]any)
	if items[dock.PreferenceWindowLayout] != encoded {
		t.Fatalf("stored layout after refused writes = %v, want the accepted value", items)
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

// failureMessage reads one refusal's message.
func failureMessage(answer front.Answer) string {
	failure, _ := answer.Decoded["error"].(map[string]any)
	message, _ := failure["message"].(string)
	return message
}

// assertLayoutOnly checks one stored value carries the layout collection and
// nothing else.
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

// assertNoSessionStateText scans one stored layout value for a session-only
// name, so a value that is not a layout at all cannot slip through.
func assertNoSessionStateText(t *testing.T, label, value string) {
	t.Helper()
	for _, name := range sessionStateNames {
		if strings.Contains(value, `"`+name+`"`) {
			t.Fatalf("%s carries the session field %q: %s", label, name, value)
		}
	}
}

// assertNoSessionState walks one cluster object's document and fails on any
// session-only name.
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
