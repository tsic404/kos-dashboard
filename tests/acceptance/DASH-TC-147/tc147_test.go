// Package dashtc147 verifies DASH-TC-147: a crashed window enters its error
// state and offers restart and close; a restart recovers the application while
// the window keeps its instance key; the taskbar lists only the current
// workspace's windows; and none of it changes the application's phase or
// replica count (DASH-AC-69, DASH-C-74, DASH-C-30).
package dashtc147

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tsic404/kosd/pkg/app"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

const (
	applicationName = "media"
	definitionName  = "media-template"
	ownerName       = "owner"
	ownerPassword   = "owner-password"
)

// mediaTemplate renders the workload the application owns, so the case has a
// downstream workload every window interaction must leave alone.
const mediaTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Name }}
  namespace: {{ .Namespace }}
spec:
  replicas: 2
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

// open registers one window of the application.
func open(t *testing.T, desktop *front.Front, workspace string) front.Answer {
	t.Helper()
	body := map[string]any{"app": applicationName}
	if workspace != "" {
		body["workspace"] = workspace
	}
	answer := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows", body)
	if answer.Status != http.StatusAccepted {
		t.Fatalf("open = %d %v", answer.Status, answer.Body)
	}
	return answer
}

// taskbarIDs renders the identities the taskbar carries, in order.
func taskbarIDs(answer front.Answer) string {
	raw, _ := answer.Decoded["taskbar"].([]any)
	ids := ""
	for _, item := range raw {
		window, _ := item.(map[string]any)
		if window == nil {
			continue
		}
		if ids != "" {
			ids += ","
		}
		ids += window["id"].(string)
	}
	return ids
}

// TestTC147ErrorRecoveryAndWorkspace is the case DASH-TC-147 describes.
func TestTC147ErrorRecoveryAndWorkspace(t *testing.T) {
	cluster := harness.Start(t)
	cluster.InstallDefinition(t, harness.DefinitionWithTemplate(t, definitionName, mediaTemplate, nil))
	cluster.CreateApplication(t, applicationName, definitionName, harness.Namespace)
	controller := cluster.ApplicationController(t, app.Options{})
	if phase := cluster.SettleApplication(t, controller, applicationName, 6); phase != app.PhaseReady {
		t.Fatalf("application phase = %q, want %q", phase, app.PhaseReady)
	}
	shell := cluster.StartShell(t)
	shell.SignInOwner(t, ownerName, ownerPassword)
	upstream, err := url.Parse(shell.Accounts.HTTP.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	desktop := front.Start(t, upstream,
		front.Contribution(t, front.Manifest(applicationName, "multi", "normal", 1024, 720, "/media")))
	before := clusterState(t, cluster)

	mainWindow := front.Window(t, open(t, desktop, ""))
	workWindow := front.Window(t, open(t, desktop, "work"))
	mainID := mainWindow["id"].(string)
	workID := workWindow["id"].(string)
	if ids := taskbarIDs(desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)); ids != workID {
		t.Fatalf("taskbar in the work workspace = %q, want only its window", ids)
	}

	// The taskbar lists only the current workspace's windows.
	switched := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/workspaces", map[string]any{"workspace": "main"})
	if switched.Status != http.StatusOK || taskbarIDs(switched) != mainID {
		t.Fatalf("switch to the default workspace = %d %v, want only its window", switched.Status, switched.Body)
	}

	// A crashed window enters its error state, where it offers restart and
	// close (DASH-C-74).
	crashed := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows/"+mainID+"/crash", map[string]any{"reason": "renderer crashed"})
	if crashed.Status != http.StatusOK {
		t.Fatalf("crash = %d %v", crashed.Status, crashed.Body)
	}
	if window := front.Window(t, crashed); window["state"] != "error" || window["errorReason"] != "renderer crashed" {
		t.Fatalf("crashed window = %v, want the error state with its reason", window)
	}
	if ids := taskbarIDs(desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)); ids != "" {
		t.Fatalf("taskbar after the crash = %q, want no running window in the current workspace", ids)
	}

	// A restart recovers the application and keeps the window's instance key.
	restarted := desktop.Call(http.MethodPost, "/api/v1/desktop/windows/"+mainID+"/restart", nil)
	if restarted.Status != http.StatusAccepted {
		t.Fatalf("restart = %d %v", restarted.Status, restarted.Body)
	}
	recovered := front.Window(t, restarted)
	if recovered["state"] != "normal" || recovered["id"] != mainID || recovered["instance"] != mainWindow["instance"] {
		t.Fatalf("restarted window = %v, want the same instance key at normal", recovered)
	}
	instanceOne, _ := restarted.Decoded["instance"].(map[string]any)
	if instanceOne == nil {
		t.Fatalf("restart answer carries no application instance: %v", restarted.Decoded)
	}
	if ids := taskbarIDs(desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)); ids != mainID {
		t.Fatalf("taskbar after the restart = %q, want the recovered window", ids)
	}

	// Closing the work window removes it from its own workspace only.
	if closed := desktop.Call(http.MethodDelete, "/api/v1/desktop/windows/"+workID, nil); closed.Status != http.StatusOK {
		t.Fatalf("close the work window = %d %v", closed.Status, closed.Body)
	}
	if ids := taskbarIDs(desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)); ids != mainID {
		t.Fatalf("taskbar after closing the work window = %q, want the recovered window", ids)
	}

	// A window that stops sending heartbeats enters the error state too.
	heartbeat := desktop.Call(http.MethodPost, "/api/v1/desktop/windows/"+mainID+"/heartbeat", nil)
	if heartbeat.Status != http.StatusOK {
		t.Fatalf("heartbeat = %d %v", heartbeat.Status, heartbeat.Body)
	}
	desktop.Clock.Advance(31 * time.Second)
	expired := desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)
	windows := front.Windows(expired)
	if len(windows) != 1 || windows[0]["state"] != "error" || windows[0]["errorReason"] == "" {
		t.Fatalf("windows after the heartbeat timeout = %v, want the silent window in error", windows)
	}

	// Nothing in the scenario moved the application or its workload.
	if current := clusterState(t, cluster); current != before {
		t.Fatalf("cluster after the window scenario = %q, want %q", current, before)
	}
}

// clusterState renders the application's phase and version and every workload
// as one string, so any write the window interaction made shows up.
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
