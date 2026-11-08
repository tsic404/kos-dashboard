// Package kosdtc175 verifies KOSD-TC-175: a postMessage from an application
// frame is judged against the window instance that frame mounts. A message that
// names another instance is ignored — it obtains no token, renews no liveness and
// vetoes no close — while the frame's own reports still work; an application that
// stops reporting enters the error state inside the judgement window
// (KOSD-AC-71, KOSD-C-81).
//
// The case runs the browser shell itself: this test starts the real front over
// the control plane and hands its address and signed-in session to the Node
// process that loads the shell modules. The assertions read the front's window
// session, so the judged state is the front's, not a stub's.
package kosdtc175

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tsic404/kosd/pkg/app"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

const (
	applicationName = "media"
	definitionName  = "media-template"
	ownerName       = "owner"
	ownerPassword   = "owner-password"
	manifestVersion = "1.0.0"
)

// mediaTemplate renders the workload the application owns, so the case has a
// downstream object the window traffic must leave alone.
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

// TestKOSDTC175InstanceCheck is the case KOSD-TC-175 describes.
func TestKOSDTC175InstanceCheck(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("the browser half of this case needs node: %v", err)
	}
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

	// The open answer carries the signed frame address the shell mounts, so the
	// case needs an asset store and not a fabricated address (KOSD-C-73).
	manifest := front.Manifest(applicationName, "multi", "normal", 1024, 720)
	contribution := front.Contribution(t, manifest)
	assets := apphost.NewMemoryAssets()
	if err := assets.Publish(contribution.Extension, manifestVersion, contribution.Digest,
		map[string][]byte{"index.html": []byte("<!doctype html><title>media</title>")}); err != nil {
		t.Fatalf("publish the application assets: %v", err)
	}
	desktop := front.StartWithAssets(t, upstream, assets, contribution)
	before := clusterState(t, cluster)

	// The shell judges liveness against the front's clock. The case moves that
	// clock between its steps rather than waiting out the judgement window.
	clock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/advance" {
			http.NotFound(w, r)
			return
		}
		seconds, err := strconv.Atoi(r.URL.Query().Get("seconds"))
		if err != nil || seconds <= 0 {
			http.Error(w, "seconds must be a positive integer", http.StatusBadRequest)
			return
		}
		desktop.Clock.Advance(time.Duration(seconds) * time.Second)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(clock.Close)

	command := exec.Command("node", "--test", "shell.test.js")
	command.Env = append(os.Environ(),
		"KOSD_TC175_FRONT="+desktop.Server.URL,
		"KOSD_TC175_CLOCK="+clock.URL,
		"KOSD_TC175_COOKIE="+desktop.Cookies.Get("Cookie"),
		"KOSD_TC175_CSRF="+desktop.Cookies.Get("X-CSRF-Token"),
		"KOSD_TC175_APP="+applicationName,
		"KOSD_TC175_NOW="+desktop.Clock.Now().UTC().Format(time.RFC3339),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("the browser half of the case failed: %v\n%s", err, output)
	}
	// The browser half skips itself when one of its inputs is missing, and the
	// runner still exits zero: a renamed variable would otherwise pass this case
	// with no assertion run at all. Read the runner's own summary.
	requireBrowserCaseRan(t, output)

	// No window message touched the application or its workload (DASH-C-30).
	if current := clusterState(t, cluster); current != before {
		t.Fatalf("cluster after the window traffic = %q, want %q", current, before)
	}
}

// nodeSummary matches the test runner's own count lines, so the case can read
// how many tests passed and were skipped instead of trusting the exit code.
var nodeSummary = regexp.MustCompile(`(?m)^(?:ℹ |# )?(pass|fail|skipped) (\d+)$`)

// requireBrowserCaseRan fails when the Node process ran no assertion. The
// runner exits zero on a file whose only test is skipped, so exit status alone
// cannot tell a passing case from a case that never ran.
func requireBrowserCaseRan(t *testing.T, output []byte) {
	t.Helper()
	counts := map[string]int{}
	for _, match := range nodeSummary.FindAllStringSubmatch(string(output), -1) {
		value, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		counts[match[1]] = value
	}
	if counts["pass"] == 0 || counts["fail"] != 0 || counts["skipped"] != 0 {
		t.Fatalf("the browser half reported %d passed, %d failed, %d skipped; want a passed case and no skipped one:\n%s",
			counts["pass"], counts["fail"], counts["skipped"], output)
	}
}

// clusterState renders the application's phase and version and every workload
// as one string, so any write the window traffic made shows up.
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
