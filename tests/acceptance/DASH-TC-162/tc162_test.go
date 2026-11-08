// Package dashtc162 verifies DASH-TC-162: an installed extension declares a
// page and an action, the page appears through the browser front with the
// title, location, required role, source types and actions it declared, every
// item carries its current value, source, modification entry or read-only
// reason, the action submits a control-plane write that answers accepted with
// the object reference and repeats idempotently, progress and terminal state
// are read from the object's own status and conditions, disabling the
// component withdraws the page and the action, and a page never carries a
// frame (DASH-AC-71, DASH-C-22, KOSD-C-12, KOSD-C-13, KOSD-C-53).
package dashtc162

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/tsic404/kosd/pkg/controlplane"
	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/pkg/identity"
	"github.com/tsic404/kosd/pkg/pages"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/bff"
)

const (
	componentName = "reports"
	extensionID   = "reports-ext"
	pageID        = "reports"
	readOnlyID    = "reports-readonly"
	actionCreate  = "create-report"
	reportGroup   = "reports.kos.tsic.top"
	reportVersion = "v1"
	reportKind    = "Report"
	reportPlural  = "reports"
	reportName    = "ink"
	ownerName     = "owner"
	ownerPassword = "owner-password"
	memberName    = "member"
	memberPass    = "member-password"
)

// reportGVR is the class the page action writes.
var reportGVR = schema.GroupVersionResource{Group: reportGroup, Version: reportVersion, Resource: reportPlural}

// declaration is the extension the case installs: one object class, one
// writable page that declares an action, and one read-only page that names all
// seven source types of KOSD-C-53.
func declaration() extension.Declaration {
	return extension.Declaration{
		Identifier: extensionID,
		Version:    "1.0.0",
		APIGroup:   reportGroup,
		Contributions: extension.Contributions{
			ObjectClasses: []extension.ObjectClass{{
				Group: reportGroup, Kind: reportKind, Resource: reportPlural,
				Versions: []string{reportVersion}, Namespaced: true,
			}},
			Pages: []extension.Page{
				{
					ID: pageID, Title: "报表", Location: "panel",
					RequiredRole: string(write.RoleMember),
					Items: []extension.PageItem{{
						ID: "reports", Label: "报表", Source: extension.PageSourceObject,
						Reference: reportKind, Action: actionCreate,
					}},
					Actions: []extension.Action{{
						ID: actionCreate, Method: http.MethodPost,
						RequiredRole: string(write.RoleOwner), Path: reportCollectionPath(),
					}},
				},
				{
					ID: readOnlyID, Title: "只读报表", Location: "panel",
					ReadOnly: true, ReadOnlyReason: "报表只读展示",
					Items: []extension.PageItem{
						{ID: "object", Label: "对象", Source: extension.PageSourceObject, Reference: reportKind},
						{ID: "computed", Label: "汇总", Source: extension.PageSourceComputed, Reference: "report-count"},
						{ID: "metric", Label: "指标", Source: extension.PageSourceMetric, Reference: "report-cpu"},
						{ID: "probe", Label: "探测", Source: extension.PageSourceProbe, Reference: "report-probe"},
						{ID: "read", Label: "日志", Source: extension.PageSourceRead, Reference: "report-logs"},
						{ID: "local", Label: "本机", Source: extension.PageSourceLocal, Store: "reports.view"},
						{ID: "database", Label: "数据库", Source: extension.PageSourceDatabase, Reference: "report-records"},
					},
				},
			},
		},
		Permissions: []extension.Permission{
			{Contribution: pageID, RequiredRole: string(write.RoleMember)},
			{Contribution: reportKind, ObjectWriters: []write.Role{write.RoleOwner}},
		},
		Retention: []extension.Retention{{Resource: reportPlural, Keep: true}},
	}
}

// reportCollectionPath is the create the page declares (KOSD-C-12). The path
// names its namespace literally: the page projection carries no page- or
// item-level namespace for the shell to fill a placeholder with.
func reportCollectionPath() string {
	return "/api/v1/resources/" + reportKind + "?apiVersion=" + reportGroup + "/" + reportVersion +
		"&namespace=" + harness.Namespace
}

// reportManifest builds one object of the class the page writes.
func reportManifest(name string) map[string]any {
	return map[string]any{
		"apiVersion": reportGroup + "/" + reportVersion,
		"kind":       reportKind,
		"metadata":   map[string]any{"name": name, "namespace": harness.Namespace},
		"spec":       map[string]any{"title": "月度报表"},
	}
}

// readings resolves the server-source items of the declared pages. It reads the
// cluster the control plane owns, which is what a product reader does; the
// browser still sees the value only through the page projection.
type readings struct {
	cluster *harness.Cluster
	now     func() time.Time
}

// Read implements pages.Readings.
func (r readings) Read(ctx context.Context, _ identity.Session, item extension.PageItem) (pages.Reading, *controlplane.Error) {
	if item.Reference == reportKind {
		list, err := r.cluster.Dynamic.Resource(reportGVR).Namespace(harness.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return pages.Reading{}, &controlplane.Error{
				Code: controlplane.CodeInternal, Message: err.Error(),
				Stage: controlplane.StageRead, SideEffects: controlplane.SideEffectsNone,
			}
		}
		values := make([]map[string]any, 0, len(list.Items))
		for _, report := range list.Items {
			title, _, _ := unstructured.NestedString(report.Object, "spec", "title")
			values = append(values, map[string]any{"name": report.GetName(), "title": title})
		}
		return pages.Reading{Value: values, ObservedAt: r.now()}, nil
	}
	return pages.Reading{Value: map[string]any{"reference": item.Reference, "source": item.Source}, ObservedAt: r.now()}, nil
}

// summary is one page entry of the registry read.
type summary struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Location       string   `json:"location"`
	RequiredRole   string   `json:"requiredRole"`
	ReadOnly       bool     `json:"readOnly"`
	ReadOnlyReason string   `json:"readOnlyReason"`
	Sources        []string `json:"sources"`
	Items          []struct {
		ID     string `json:"id"`
		Label  string `json:"label"`
		Source string `json:"source"`
		Store  string `json:"store"`
	} `json:"items"`
	Actions []struct {
		ID    string `json:"id"`
		Entry struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"entry"`
	} `json:"actions"`
}

// item is one rendered item of a page read.
type item struct {
	ID         string          `json:"id"`
	Label      string          `json:"label"`
	Source     string          `json:"source"`
	Value      json.RawMessage `json:"value"`
	Store      string          `json:"store"`
	ObservedAt *time.Time      `json:"observedAt"`
	Stale      bool            `json:"stale"`
	Action     string          `json:"action"`
	Entry      *struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	} `json:"entry"`
	Reason string `json:"reason"`
}

// rendered is one page read.
type rendered struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Location       string   `json:"location"`
	RequiredRole   string   `json:"requiredRole"`
	ReadOnly       bool     `json:"readOnly"`
	ReadOnlyReason string   `json:"readOnlyReason"`
	Sources        []string `json:"sources"`
	Items          []item   `json:"items"`
	Actions        []struct {
		ID    string `json:"id"`
		Entry struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"entry"`
	} `json:"actions"`
}

// writeAnswer is the acceptance one write answers (KOSD-C-13).
type writeAnswer struct {
	Accepted   bool                    `json:"accepted"`
	Idempotent bool                    `json:"idempotent"`
	Ref        *controlplane.ObjectRef `json:"ref"`
}

// response is one answer the front gave.
type response struct {
	status  int
	body    string
	decoded map[string]any
	cookies []*http.Cookie
	header  http.Header
}

// TestTC162PageRendersItsDeclarationAndSubmitsItsAction is the case DASH-TC-162
// describes.
func TestTC162PageRendersItsDeclarationAndSubmitsItsAction(t *testing.T) {
	cluster := harness.Start(t)
	// The package carries the class its page writes; the case installs the
	// definition as a component install does.
	cluster.InstallCRD(t, reportGroup, reportVersion, reportKind, reportPlural, true)

	artifacts := extension.MemoryArtifacts{}
	installation := cluster.InstallPackage(t, componentName, declaration(), artifacts)
	result, found := installation.Component(componentName)
	if !found || result.Phase != extension.PhaseReady || len(result.Problems) > 0 {
		t.Fatalf("component = %+v (found %v), want Ready with no contribution problem", result, found)
	}

	shell := cluster.StartShell(t, func(config *pages.Config) {
		// The reader observes at request time, so the page shell stamps and
		// judges freshness against the same clock.
		config.Now = time.Now
		config.Readings = readings{cluster: cluster, now: time.Now}
	})
	shell.Use(installation)
	owner := shell.SignInOwner(t, ownerName, ownerPassword)
	shell.Account(t, owner, memberName, memberPass, write.RoleMember)

	front, cookies := startFront(t, startControlPlane(t, shell), ownerName, ownerPassword)

	// The page registry lists what the extension declared: title, location,
	// required permission, every source type and the executable action
	// (DASH-C-22, KOSD-C-53).
	listed, registryAnswer := readRegistry(t, front, cookies)
	page := pageOf(t, listed, pageID)
	if page.Title != "报表" || page.Location != "panel" || page.RequiredRole != string(write.RoleMember) {
		t.Fatalf("page summary = %+v, want its declared title, location and role", page)
	}
	if len(page.Sources) != 1 || page.Sources[0] != extension.PageSourceObject {
		t.Fatalf("page sources = %v, want the declared object source", page.Sources)
	}
	if len(page.Actions) != 1 || page.Actions[0].ID != actionCreate ||
		page.Actions[0].Entry.Method != http.MethodPost || page.Actions[0].Entry.Path != reportCollectionPath() {
		t.Fatalf("page actions = %+v, want the declared control-plane write", page.Actions)
	}
	readOnly := pageOf(t, listed, readOnlyID)
	if !readOnly.ReadOnly || readOnly.ReadOnlyReason != "报表只读展示" || len(readOnly.Actions) != 0 {
		t.Fatalf("read-only page = %+v, want it read-only with its reason and no write", readOnly)
	}
	if sources := sortedStrings(readOnly.Sources); strings.Join(sources, ",") != strings.Join([]string{
		extension.PageSourceComputed, extension.PageSourceDatabase, extension.PageSourceLocal,
		extension.PageSourceMetric, extension.PageSourceObject, extension.PageSourceProbe,
		extension.PageSourceRead,
	}, ",") {
		t.Fatalf("read-only page sources = %v, want all seven source types", sources)
	}

	// Reading one page renders the server projection: every item carries its
	// current value, its source and either a modification entry or a reason
	// (KOSD-C-53).
	renderedPage, pageAnswer := readPage(t, front, pageID, cookies)
	if renderedPage.Title != "报表" || renderedPage.RequiredRole != string(write.RoleMember) {
		t.Fatalf("page read = %+v, want the declared title and role", renderedPage)
	}
	entries := itemOf(t, renderedPage, "reports")
	if entries.Value == nil || string(entries.Value) != "[]" {
		t.Fatalf("reports item value = %s, want the empty projection", entries.Value)
	}
	if entries.Source != extension.PageSourceObject || entries.ObservedAt == nil || entries.Stale {
		t.Fatalf("reports item = %+v, want a fresh object-source observation", entries)
	}
	if entries.Action != actionCreate || entries.Entry == nil ||
		entries.Entry.Method != http.MethodPost || entries.Entry.Path != reportCollectionPath() {
		t.Fatalf("reports item = %+v, want the declared modification entry", entries)
	}
	if entries.Reason != "" {
		t.Fatalf("reports item = %+v, want no reason next to a modification entry", entries)
	}

	readOnlyPage, readOnlyAnswer := readPage(t, front, readOnlyID, cookies)
	if !readOnlyPage.ReadOnly || readOnlyPage.ReadOnlyReason != "报表只读展示" || len(readOnlyPage.Actions) != 0 {
		t.Fatalf("read-only page read = %+v, want it read-only with its reason and no write", readOnlyPage)
	}
	for _, entry := range readOnlyPage.Items {
		if entry.Source == extension.PageSourceLocal {
			if entry.Store != "reports.view" || (len(entry.Value) > 0 && string(entry.Value) != "null") {
				t.Fatalf("local item = %+v, want the browser store and no server value", entry)
			}
			continue
		}
		if entry.Value == nil || entry.ObservedAt == nil {
			t.Fatalf("item %q = %+v, want a server value with its observation", entry.ID, entry)
		}
		if entry.Entry != nil || entry.Action != "" {
			t.Fatalf("item %q = %+v, a read-only page offers no modification entry", entry.ID, entry)
		}
	}
	// The same judgement over the decoded body: a field the typed structs omit
	// would otherwise pass unseen.
	if carriesKey(readOnlyAnswer, "entry") {
		t.Fatalf("read-only page answer carries a modification entry: %v", readOnlyAnswer)
	}
	assertNoCredentialFields(t, "read-only page", readOnlyAnswer)

	// A repeated write is accepted twice with one object: the action is the
	// control-plane write and never forks a second object (KOSD-C-12).
	method, path := harness.PageAction(t, declaration().Contributions.Pages[0], actionCreate, "", harness.Namespace)
	body := encode(t, reportManifest(reportName))
	accepted := call(t, front, method, path, cookies, body, cookies.Get("X-CSRF-Token"))
	if accepted.status != http.StatusAccepted {
		t.Fatalf("submit the action = %d %v, want %d", accepted.status, accepted.body, http.StatusAccepted)
	}
	answer := decodeWrite(t, accepted)
	if !answer.Accepted || answer.Ref == nil || answer.Ref.Kind != reportKind ||
		answer.Ref.Namespace != harness.Namespace || answer.Ref.Name != reportName || answer.Idempotent {
		t.Fatalf("action answer = %+v, want accepted with the created object reference", answer)
	}
	// The acceptance is not a completion: no task, job or progress identifier
	// travels with it (KOSD-C-12).
	for _, field := range []string{"task", "taskId", "job", "jobId", "progress", "progressUrl", "completed"} {
		if _, found := accepted.decoded[field]; found {
			t.Fatalf("action answer carries %q, want the object reference alone: %v", field, accepted.decoded)
		}
	}
	repeated := call(t, front, method, path, cookies, encode(t, reportManifest(reportName)), cookies.Get("X-CSRF-Token"))
	if repeated.status != http.StatusAccepted {
		t.Fatalf("repeat the action = %d %v, want %d", repeated.status, repeated.body, http.StatusAccepted)
	}
	repeat := decodeWrite(t, repeated)
	if !repeat.Accepted || !repeat.Idempotent || repeat.Ref == nil || repeat.Ref.Name != reportName {
		t.Fatalf("repeated action answer = %+v, want the same object accepted idempotently", repeat)
	}
	if count := countReports(t, cluster); count != 1 {
		t.Fatalf("reports after the repeated write = %d, want one", count)
	}
	// No separate task store exists: the write path is the object path alone.
	for _, path := range []string{"/api/v1/tasks", "/api/v1/jobs"} {
		if refused := call(t, front, http.MethodGet, path, cookies, nil, ""); refused.status != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want %d: there is no task store", path, refused.status, http.StatusNotFound)
		}
	}

	// Progress and terminal state are read from the object's status and
	// conditions, which the extension controller writes (KOSD-C-12, KOSD-C-07).
	writeReportStatus(t, cluster, reportName, map[string]any{
		"phase": "Reconciled",
		"conditions": []any{map[string]any{
			"type": "Ready", "status": "True", "reason": "Rendered",
			"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
		}},
	})
	object := readObject(t, front, cookies, reportKind, reportName)
	phase, _, _ := unstructured.NestedString(object, "status", "phase")
	if phase != "Reconciled" {
		t.Fatalf("object status phase = %q, want the state the controller wrote", phase)
	}
	conditions, _, _ := unstructured.NestedSlice(object, "status", "conditions")
	if len(conditions) != 1 || conditions[0].(map[string]any)["type"] != "Ready" {
		t.Fatalf("object conditions = %v, want the terminal condition of the object", conditions)
	}

	// The page now shows the object it wrote, so the item value comes from the
	// declared source rather than from a shell-side store (KOSD-C-53).
	afterPage, afterAnswer := readPage(t, front, pageID, cookies)
	after := itemOf(t, afterPage, "reports")
	if !strings.Contains(string(after.Value), reportName) {
		t.Fatalf("reports item after the action = %s, want the created object", after.Value)
	}
	assertNoCredentialFields(t, "page after the action", afterAnswer)

	// The browser receives no cluster credential (KOSD-F-9). The check runs over
	// the decoded bodies, not the typed structs, so a forbidden field the structs
	// omit still fails it.
	assertNoCredentialFields(t, "registry", registryAnswer)
	assertNoCredentialFields(t, "page", pageAnswer)

	// One page read through the front answers exactly what the control-plane
	// API answers for the same session (KOSD-C-12): the front aggregates, it
	// does not carry a page projection of its own.
	assertSamePageRead(t, front, cookies, shell, owner, pageID)

	// A page is not an `App`: opening one as a window application is refused,
	// and no frame address ever accompanies a page (DASH-C-22, KOSD-C-18).
	opened := call(t, front, http.MethodPost, "/api/v1/apps/"+pageID+"/windows", cookies, nil, cookies.Get("X-CSRF-Token"))
	if opened.status != http.StatusConflict {
		t.Fatalf("open a page as a window application = %d %v, want %d", opened.status, opened.body, http.StatusConflict)
	}
	if _, found := opened.decoded["frame"]; found {
		t.Fatalf("page window answer carries a frame: %v", opened.decoded)
	}
	for _, field := range []string{"frame", "frameError", "sandbox", "iframe"} {
		if carriesKey(pageAnswer, field) || carriesKey(registryAnswer, field) {
			t.Fatalf("page answers carry %q, want pages to create no iframe", field)
		}
	}

	// Disabling the component withdraws the page and the action (KOSD-AC-51).
	cluster.SetComponentDisabled(t, componentName, true)
	disabled := cluster.ExtensionState(t, artifacts)
	shell.Use(disabled)
	if withdrawn, _ := readRegistry(t, front, cookies); hasPage(withdrawn, pageID) || hasPage(withdrawn, readOnlyID) {
		t.Fatalf("pages after disabling the component = %+v, want their entries gone", withdrawn)
	}
	if refused := call(t, front, http.MethodGet, "/api/v1/pages/"+pageID, cookies, nil, ""); refused.status != http.StatusNotFound {
		t.Fatalf("disabled page = %d %v, want %d", refused.status, refused.body, http.StatusNotFound)
	}
	refused := call(t, front, method, path, cookies, encode(t, reportManifest("refused")), cookies.Get("X-CSRF-Token"))
	if refused.status != http.StatusUnprocessableEntity || stringField(refused.decoded, "error", "code") != controlplane.CodeCapabilityMissing {
		t.Fatalf("action against the disabled component = %d %v, want %d %s",
			refused.status, refused.body, http.StatusUnprocessableEntity, controlplane.CodeCapabilityMissing)
	}

	// Re-enabling restores the page and the object it wrote (KOSD-C-28).
	cluster.SetComponentDisabled(t, componentName, false)
	enabled := cluster.ExtensionState(t, artifacts)
	shell.Use(enabled)
	if restored := call(t, front, http.MethodGet, "/api/v1/pages/"+pageID, cookies, nil, ""); restored.status != http.StatusOK {
		t.Fatalf("re-enabled page = %d %v, want %d", restored.status, restored.body, http.StatusOK)
	}
	if count := countReports(t, cluster); count != 1 {
		t.Fatalf("reports after re-enabling the component = %d, want the created object kept", count)
	}
}

// assertSamePageRead compares one page read through the front with the same read
// made directly against the control-plane API (KOSD-C-12).
func assertSamePageRead(t *testing.T, front *httptest.Server, cookies http.Header, shell *harness.Shell, token, id string) {
	t.Helper()
	proxied := call(t, front, http.MethodGet, "/api/v1/pages/"+id, cookies, nil, "")
	if proxied.status != http.StatusOK {
		t.Fatalf("proxied page read = %d %v", proxied.status, proxied.body)
	}
	status, direct := shell.Call(t, http.MethodGet, "/api/v1/pages/"+id, nil, token)
	if status != http.StatusOK {
		t.Fatalf("direct page read = %d %v", status, direct)
	}
	proxiedItems, _ := proxied.decoded["items"].([]any)
	directItems, _ := direct["items"].([]any)
	if len(proxiedItems) != len(directItems) || len(directItems) == 0 {
		t.Fatalf("proxied items = %d, direct items = %d, want the same read", len(proxiedItems), len(directItems))
	}
	for index := range directItems {
		// A read-time source stamps each read with the moment it observed, so
		// the comparison covers the projection and not that stamp.
		if !sameJSON(withoutObservation(proxiedItems[index]), withoutObservation(directItems[index])) {
			t.Fatalf("proxied item %d = %v, want the direct read's %v", index, proxiedItems[index], directItems[index])
		}
	}
}

// withoutObservation drops the observation stamp of one rendered item, which a
// read-time source moves with every read.
func withoutObservation(value any) any {
	item, ok := value.(map[string]any)
	if !ok {
		return value
	}
	copied := make(map[string]any, len(item))
	for key, nested := range item {
		if key == "observedAt" {
			continue
		}
		copied[key] = nested
	}
	return copied
}

// sameJSON compares two decoded values by their JSON encoding.
func sameJSON(left, right any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return bytes.Equal(leftRaw, rightRaw)
}

// startControlPlane composes the harness control plane with the application
// registry route the front reads. The extension of this case declares no `App`,
// so the registry is empty and every entry stays a page.
func startControlPlane(t *testing.T, shell *harness.Shell) *httptest.Server {
	t.Helper()
	target, err := url.Parse(shell.Accounts.HTTP.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "notReady": []any{}})
	})
	mux.Handle("/", proxy)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// startFront serves the browser front over the composed control plane and logs
// one account in through it.
func startFront(t *testing.T, control *httptest.Server, username, password string) (*httptest.Server, http.Header) {
	t.Helper()
	upstream, err := url.Parse(control.URL)
	if err != nil {
		t.Fatalf("parse the composed URL: %v", err)
	}
	server, err := bff.New(bff.Config{Upstream: upstream})
	if err != nil {
		t.Fatalf("build the browser front: %v", err)
	}
	front := httptest.NewServer(server.Handler())
	t.Cleanup(front.Close)
	response := call(t, front, http.MethodPost, "/api/v1/auth/login", http.Header{},
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`), "")
	if response.status != http.StatusOK {
		t.Fatalf("login through the front = %d %v", response.status, response.body)
	}
	if _, found := response.decoded["token"]; found {
		t.Fatalf("the login answer carries the control-plane token: %v", response.decoded)
	}
	cookies := http.Header{}
	pairs := make([]string, 0, 2)
	for _, cookie := range response.cookies {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
	}
	cookies.Set("Cookie", strings.Join(pairs, "; "))
	for _, cookie := range response.cookies {
		if cookie.Name == "kos_bff_csrf" {
			cookies.Set("X-CSRF-Token", cookie.Value)
		}
	}
	if cookies.Get("X-CSRF-Token") == "" {
		t.Fatal("login set no CSRF token")
	}
	return front, cookies
}

// call sends one request to the front.
func call(t *testing.T, front *httptest.Server, method, path string, cookies http.Header, body io.Reader, csrf string) response {
	t.Helper()
	request, err := http.NewRequest(method, front.URL+path, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if cookie := cookies.Get("Cookie"); cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	answer, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer answer.Body.Close()
	payload, _ := io.ReadAll(answer.Body)
	decoded := map[string]any{}
	_ = json.Unmarshal(payload, &decoded)
	return response{status: answer.StatusCode, body: string(payload), decoded: decoded, cookies: answer.Cookies(), header: answer.Header}
}

// readRegistry reads the page registry the navigation is built from, and the
// body as the browser decoded it, so a check over the raw answer sees every
// field the typed structs omit.
func readRegistry(t *testing.T, front *httptest.Server, cookies http.Header) ([]summary, any) {
	t.Helper()
	answer := call(t, front, http.MethodGet, "/api/v1/pages", cookies, nil, "")
	if answer.status != http.StatusOK {
		t.Fatalf("read the page registry = %d %v", answer.status, answer.body)
	}
	var listed struct {
		Items []summary `json:"items"`
	}
	if err := json.Unmarshal([]byte(answer.body), &listed); err != nil {
		t.Fatalf("decode the page registry: %v", err)
	}
	return listed.Items, any(answer.decoded)
}

// pageOf resolves one listed page.
func pageOf(t *testing.T, items []summary, id string) summary {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("page registry = %+v, want page %q", items, id)
	return summary{}
}

// hasPage reports whether the registry lists one page.
func hasPage(items []summary, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

// readPage reads one rendered page for the session.
func readPage(t *testing.T, front *httptest.Server, id string, cookies http.Header) (rendered, any) {
	t.Helper()
	answer := call(t, front, http.MethodGet, "/api/v1/pages/"+id, cookies, nil, "")
	if answer.status != http.StatusOK {
		t.Fatalf("read page %s = %d %v", id, answer.status, answer.body)
	}
	var page rendered
	if err := json.Unmarshal([]byte(answer.body), &page); err != nil {
		t.Fatalf("decode page %s: %v", id, err)
	}
	return page, any(answer.decoded)
}

// itemOf resolves one rendered item.
func itemOf(t *testing.T, page rendered, id string) item {
	t.Helper()
	for _, entry := range page.Items {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("page %s items = %+v, want item %q", page.ID, page.Items, id)
	return item{}
}

// readObject reads one object through the control-plane resource route.
func readObject(t *testing.T, front *httptest.Server, cookies http.Header, kind, name string) map[string]any {
	t.Helper()
	path := "/api/v1/resources/" + kind + "/" + name + "?apiVersion=" + reportGroup + "/" + reportVersion +
		"&namespace=" + harness.Namespace
	answer := call(t, front, http.MethodGet, path, cookies, nil, "")
	if answer.status != http.StatusOK {
		t.Fatalf("read %s = %d %v", path, answer.status, answer.body)
	}
	object, _ := answer.decoded["object"].(map[string]any)
	if object == nil {
		t.Fatalf("read %s carries no object: %v", path, answer.decoded)
	}
	return object
}

// encode renders one request body.
func encode(t *testing.T, body any) io.Reader {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode the body: %v", err)
	}
	return bytes.NewReader(raw)
}

// decodeWrite reads the acceptance one write answered.
func decodeWrite(t *testing.T, answer response) writeAnswer {
	t.Helper()
	var decoded writeAnswer
	if err := json.Unmarshal([]byte(answer.body), &decoded); err != nil {
		t.Fatalf("decode the write answer: %v", err)
	}
	return decoded
}

// countReports counts the objects of the class the page writes.
func countReports(t *testing.T, cluster *harness.Cluster) int {
	t.Helper()
	list, err := cluster.Dynamic.Resource(reportGVR).Namespace(harness.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	return len(list.Items)
}

// writeReportStatus writes one object's status the way the extension controller
// does, so progress and terminal state have a single source: the object.
func writeReportStatus(t *testing.T, cluster *harness.Cluster, name string, status map[string]any) {
	t.Helper()
	ctx := context.Background()
	resource := cluster.Dynamic.Resource(reportGVR).Namespace(harness.Namespace)
	report, err := resource.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read report %s: %v", name, err)
	}
	report.Object["status"] = status
	if _, err := resource.Update(ctx, report, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("write the status of report %s: %v", name, err)
	}
}

// sortedStrings sorts one string list.
func sortedStrings(values []string) []string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return copied
}

// stringField reads one nested string out of a decoded answer.
func stringField(decoded map[string]any, keys ...string) string {
	current := any(decoded)
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	value, _ := current.(string)
	return value
}

// forbiddenCredentialFields are the field fragments a browser answer must never
// name (KOSD-F-9). Matching is by containment on the normalized name, so a key
// such as kubeconfigData or service_account_token_data cannot slip past.
var forbiddenCredentialFields = []string{"kubeconfig", "serviceaccounttoken", "clientkeydata", "clientcertificatedata"}

// normalizeField folds a field name for comparison: case, dashes and
// underscores carry no meaning in a credential key.
func normalizeField(name string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), "_", ""))
}

// walkFields visits every field name of one decoded JSON value, at any depth.
func walkFields(value any, visit func(string)) {
	switch typed := value.(type) {
	case map[string]any:
		for name, nested := range typed {
			visit(name)
			walkFields(nested, visit)
		}
	case []any:
		for _, nested := range typed {
			walkFields(nested, visit)
		}
	}
}

// assertNoCredentialFields checks that no answer the browser receives carries a
// kubeconfig or a service-account token (KOSD-F-9).
func assertNoCredentialFields(t *testing.T, label string, value any) {
	t.Helper()
	walkFields(value, func(name string) {
		normalized := normalizeField(name)
		for _, forbidden := range forbiddenCredentialFields {
			if strings.Contains(normalized, forbidden) {
				t.Fatalf("%s carries %q: the browser must never receive cluster credentials (KOSD-F-9)", label, name)
			}
		}
	})
}

// carriesKey reports whether one decoded answer names a key containing key, so
// a field spelled with a prefix or suffix is still found.
func carriesKey(value any, key string) bool {
	target := normalizeField(key)
	found := false
	walkFields(value, func(name string) {
		if strings.Contains(normalizeField(name), target) {
			found = true
		}
	})
	return found
}
