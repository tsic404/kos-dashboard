// Package dashtc61 verifies DASH-TC-61: opening two windows of one application,
// closing one and then closing all leaves the application's phase and replica
// count unchanged and creates or rebuilds no downstream object. The window set
// is browser session state: no cluster object and no preference value carries
// it (DASH-AC-30, DASH-C-30, DASH-C-74, KOSD-F-19).
package dashtc61

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	// preferenceDomain is the product's preference domain: window interaction
	// writes no value into it (DASH-C-27, KOSD-F-19).
	preferenceDomain = dock.Identifier
)

// mediaTemplate renders the workload the application owns, so the case has a
// downstream object every window interaction must leave alone.
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

// mediaManifest declares the window application the desktop opens.
func mediaManifest() map[string]any {
	return front.Manifest(applicationName, "multi", "normal", 1024, 720, "/media")
}

// TestTC61ClosingWindowsChangesNothing is the case DASH-TC-61 describes.
func TestTC61ClosingWindowsChangesNothing(t *testing.T) {
	cluster := harness.Start(t)
	cluster.InstallDefinition(t, harness.DefinitionWithTemplate(t, definitionName, mediaTemplate, nil))
	cluster.CreateApplication(t, applicationName, definitionName, harness.Namespace)
	controller := cluster.ApplicationController(t, app.Options{})
	if phase := cluster.SettleApplication(t, controller, applicationName, 6); phase != app.PhaseReady {
		t.Fatalf("application phase = %q, want %q", phase, app.PhaseReady)
	}

	// The product's preference domain is live while the component is active;
	// the window interaction must leave every value in it untouched.
	cluster.InstallCRD(t, dock.APIGroup, dock.EntryVersion, dock.EntryKind, dock.EntryResource, true)
	installation := cluster.InstallPackage(t, "dashboard", dock.Declaration(), extension.MemoryArtifacts{})
	shell := cluster.StartShell(t)
	shell.Use(installation)
	owner := shell.SignInOwner(t, ownerName, ownerPassword)
	upstream, err := url.Parse(shell.Accounts.HTTP.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	desktop := front.Start(t, upstream, front.Contribution(t, mediaManifest()))

	// The desktop opens two windows of the one application (DASH-TC-61).
	firstAnswer := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows",
		map[string]any{"app": applicationName})
	secondAnswer := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows",
		map[string]any{"app": applicationName})
	if firstAnswer.Status != http.StatusAccepted || secondAnswer.Status != http.StatusAccepted {
		t.Fatalf("open = %d %v / %d %v", firstAnswer.Status, firstAnswer.Body, secondAnswer.Status, secondAnswer.Body)
	}
	first, second := front.Window(t, firstAnswer), front.Window(t, secondAnswer)
	if first["id"] == second["id"] || first["instance"] == second["instance"] {
		t.Fatalf("windows = %v and %v, want two distinct instances", first, second)
	}
	listed := desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)
	if listed.Status != http.StatusOK || len(front.Windows(listed)) != 2 {
		t.Fatalf("session after two opens = %d %v, want two windows", listed.Status, listed.Body)
	}

	// Every close leaves the application and its downstream objects alone.
	before := clusterState(t, cluster)
	closed := desktop.Call(http.MethodDelete, windowPath(first["id"].(string)), nil)
	if closed.Status != http.StatusOK || len(front.Windows(closed)) != 1 {
		t.Fatalf("close one window = %d %v, want one window left", closed.Status, closed.Body)
	}
	if current := clusterState(t, cluster); current != before {
		t.Fatalf("cluster after closing one window = %q, want %q", current, before)
	}
	closed = desktop.Call(http.MethodDelete, windowPath(second["id"].(string)), nil)
	if closed.Status != http.StatusOK || len(front.Windows(closed)) != 0 {
		t.Fatalf("close all = %d %v, want no window left", closed.Status, closed.Body)
	}
	if current := clusterState(t, cluster); current != before {
		t.Fatalf("cluster after closing every window = %q, want %q", current, before)
	}

	// Reopening after every window closed creates no deployment either.
	reopened := front.Window(t, desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows",
		map[string]any{"app": applicationName}))
	if reopened["id"] == first["id"] || reopened["instance"].(float64) != 1 {
		t.Fatalf("reopened window = %v, want a fresh instance", reopened)
	}
	if current := clusterState(t, cluster); current != before {
		t.Fatalf("cluster after reopening a window = %q, want %q", current, before)
	}

	// The negative criterion: window interaction leaves the preference domain
	// without an open set, z-order, focus or minimization (KOSD-F-19).
	status, stored := shell.Call(t, http.MethodGet, "/api/v1/preferences/"+preferenceDomain, nil, owner)
	if status != http.StatusOK {
		t.Fatalf("read the preference domain = %d %v", status, stored)
	}
	items, _ := stored["items"].(map[string]any)
	if len(items) != 0 {
		t.Fatalf("preference domain after window interaction = %v, want no stored value", items)
	}
}

// windowPath addresses one desktop window.
func windowPath(window string) string {
	return "/api/v1/desktop/windows/" + window
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
