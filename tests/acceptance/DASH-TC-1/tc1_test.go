// Package dashtc1 verifies the desktop page: the four display items DASH-C-52
// declares are readable with their sources, the notification centre carries
// the same events the object route does and the account's read state follows it
// across browsers, and uninstalling an application takes its entries off the
// desktop without touching the notification history (DASH-AC-1, DASH-C-27,
// KOSD-C-53, KOSD-C-64).
package dashtc1

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/tsic404/kosd/api"
	"github.com/tsic404/kosd/api/v1beta1"
	"github.com/tsic404/kosd/pkg/app"
	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/bff"
	"github.com/tsic404/kos-dashboard/pkg/dock"
)

const (
	firstApplication  = "media"
	secondApplication = "player"
	definitionName    = "media-template"
	firstEntry        = "media-desktop"
	secondEntry       = "player-desktop"
	ownerName         = "owner"
	ownerPassword     = "owner-password"
	guestName         = "guest"
	guestPassword     = "guest-password"
)

// mediaTemplate renders the workload a case installs, so an installed
// application owns a real downstream object.
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

// TestTC1DesktopPageReadsEverySource installs two applications and a desktop
// component, reads the desktop page through the browser front and checks every
// item and source DASH-C-52 declares.
func TestTC1DesktopPageReadsEverySource(t *testing.T) {
	cluster := harness.Start(t)
	cluster.InstallCRD(t, dock.APIGroup, dock.EntryVersion, dock.EntryKind, dock.EntryResource, true)
	cluster.InstallDefinition(t, harness.DefinitionWithTemplate(t, definitionName, mediaTemplate, nil))
	for _, name := range []string{firstApplication, secondApplication} {
		cluster.CreateApplication(t, name, definitionName, harness.Namespace)
		controller := cluster.ApplicationController(t, app.Options{})
		if phase := cluster.SettleApplication(t, controller, name, 6); phase != app.PhaseReady {
			t.Fatalf("application %s phase = %q, want %q", name, phase, app.PhaseReady)
		}
	}

	installation := cluster.InstallPackage(t, "dashboard", dock.Declaration(), extension.MemoryArtifacts{})
	// Each application is reachable, so an entry has an address to open
	// (DASH-C-30, C-43).
	for name, host := range map[string]string{firstApplication: "media.test", secondApplication: "player.test"} {
		cluster.Create(t, harness.Endpoint(name+"-entry", harness.ManagedLabels(), map[string]any{
			"kind":      string(v1beta1.EndpointKindHTTPHost),
			"targetApp": name,
			"host":      host,
		}))
	}
	// One recent event gives the notification centre something to read and
	// the read state something to mark (DASH-C-52, KOSD-C-64). It concerns the
	// application the case keeps, so the uninstall at the end leaves it alone.
	installEvent(t, cluster, "player-started", secondApplication, time.Now().UTC())
	shell := cluster.StartShell(t)
	shell.Use(installation)
	owner := shell.SignInOwner(t, ownerName, ownerPassword)

	// The browser only ever talks to the front, which holds the control-plane
	// session on its behalf (KOSD-C-63).
	front, cookies := startFront(t, shell, ownerName, ownerPassword)

	for name, application := range map[string]string{firstEntry: firstApplication, secondEntry: secondApplication} {
		addEntry(t, front, cookies, name, application)
	}

	page := readDesktop(t, front, cookies)
	if page.ID != dock.PageID {
		t.Fatalf("desktop page id = %q, want %q", page.ID, dock.PageID)
	}
	if len(page.Items) != 4 {
		t.Fatalf("desktop items = %+v, want the four DASH-C-52 declares", page.Items)
	}
	for _, id := range []string{"entries", "applications", "recent", "notifications"} {
		if item := page.item(id); item.Source == "" {
			t.Fatalf("item %q carries no source (KOSD-C-53)", id)
		}
	}
	recent := page.item("recent")
	if recent.Source != "local" || recent.Store != "desktop.recent" {
		t.Fatalf("recent item = %+v, want the browser-local source", recent)
	}

	entries := page.entries(t, "entries")
	if len(entries) != 2 {
		t.Fatalf("desktop entries = %+v, want both installed applications", entries)
	}
	for _, name := range []string{firstEntry, secondEntry} {
		if !hasEntry(entries, name) {
			t.Fatalf("desktop entries = %+v, want %q", entries, name)
		}
	}
	for _, entry := range entries {
		if !entry.IsAvailable {
			t.Fatalf("entry %+v is not openable although its application is Ready", entry)
		}
	}

	applications := page.applications(t, "applications")
	for _, name := range []string{firstApplication, secondApplication} {
		if !hasApplication(applications, name) {
			t.Fatalf("installed applications = %+v, want %q", applications, name)
		}
	}

	// The notification centre carries exactly the events the object route
	// returns (KOSD-C-64, DASH-C-52).
	direct := eventsOf(t, shell, owner)
	notifications := page.notifications(t, "notifications")
	if eventNames(notifications.Events) != eventNames(direct.Events) {
		t.Fatalf("notification events = %v, want the object route's %v",
			eventNames(notifications.Events), eventNames(direct.Events))
	}
	if len(notifications.Events) == 0 {
		t.Fatal("the notification centre lists no event, so its read state cannot be observed")
	}
	if notifications.ReadThrough != "" || notifications.Unread != len(notifications.Events) || allRead(notifications) {
		t.Fatalf("notification centre = %+v, want every event unread before the account marks it", notifications)
	}

	// The page declares the write that moves the account's read state, and it
	// names the roles the control plane lets write preferences (DASH-C-27,
	// DASH-C-22).
	markRead := page.action(t, dock.ActionMarkNotificationsRead)
	if markRead.Entry.Method != http.MethodPut ||
		markRead.Entry.Path != "/api/v1/preferences/"+dock.Identifier+"/"+dock.PreferenceRead {
		t.Fatalf("mark-read action = %+v, want the account's preference route", markRead)
	}
	if markRead.RequiredRole != string(write.RoleMember) {
		t.Fatalf("mark-read role = %q, want %q: a Guest writes no preference",
			markRead.RequiredRole, write.RoleMember)
	}
	if response := call(t, front, markRead.Entry.Method, markRead.Entry.Path,
		cookies.Get("Cookie"), bytes.NewReader(markRead.Entry.Body),
		cookies.Get("X-CSRF-Token")); response.status != http.StatusNoContent {
		t.Fatalf("mark the notifications read = %d %v", response.status, response.body)
	}
	marked := readDesktop(t, front, cookies).notifications(t, "notifications")
	if marked.ReadThrough == "" || marked.Unread != 0 || !allRead(marked) {
		t.Fatalf("notification centre after marking read = %+v, want every listed event read", marked)
	}

	// The state belongs to the account, not the browser: the same account in a
	// second browser reads it back (DASH-C-27).
	otherFront, otherCookies := startFront(t, shell, ownerName, ownerPassword)
	if other := readDesktop(t, otherFront, otherCookies).notifications(t, "notifications"); other.ReadThrough != marked.ReadThrough ||
		other.Unread != 0 || !allRead(other) {
		t.Fatalf("notification centre in the second browser = %+v, want the first browser's %+v", other, marked)
	}

	// The preference stays out of the object projection (DASH-C-27).
	projection := call(t, front, http.MethodGet, applicationListPath(), cookies.Get("Cookie"), nil, "")
	for _, field := range []string{"schemaVersion", "readThrough"} {
		if strings.Contains(projection.body, field) {
			t.Fatalf("the object projection carries %q: preferences never enter objects (DASH-C-27)", field)
		}
	}

	// A Guest reads the desktop and its read state but writes none: the action
	// names Member, and both the page's write and a direct call are refused with
	// the control plane's own envelope (DASH-C-22, KOSD-C-59).
	guestToken := shell.Account(t, owner, guestName, guestPassword, write.RoleGuest)
	guestFront, guestCookies := startFront(t, shell, guestName, guestPassword)
	guestPage := readDesktop(t, guestFront, guestCookies)
	if guest := guestPage.notifications(t, "notifications"); guest.ReadThrough != "" || guest.Unread != len(guest.Events) {
		t.Fatalf("Guest notification centre = %+v, want an account that marked nothing read", guest)
	}
	if role := guestPage.action(t, dock.ActionMarkNotificationsRead).RequiredRole; role != string(write.RoleMember) {
		t.Fatalf("Guest mark-read role = %q, want %q", role, write.RoleMember)
	}
	if response := call(t, guestFront, markRead.Entry.Method, markRead.Entry.Path,
		guestCookies.Get("Cookie"), bytes.NewReader(markRead.Entry.Body),
		guestCookies.Get("X-CSRF-Token")); response.status != http.StatusForbidden {
		t.Fatalf("Guest mark-read through the page = %d %v, want %d", response.status, response.body, http.StatusForbidden)
	}
	if status, body := shell.Call(t, http.MethodPut, markRead.Entry.Path,
		map[string]any{"value": "anything"}, guestToken); status != http.StatusForbidden {
		t.Fatalf("direct Guest preference write = %d %v, want %d", status, body, http.StatusForbidden)
	}

	// One read through the front answers what the control-plane API answers
	// (KOSD-C-12).
	assertSameRead(t, front, cookies, shell, owner)

	// The browser session carries neither a kubeconfig nor a service-account
	// token (KOSD-F-9).
	assertNoCredentials(t, front, cookies)

	// Uninstalling the first application takes its entry off the desktop; the
	// other application keeps its entry and the history is untouched.
	uninstall(t, cluster, firstApplication)
	after := readDesktop(t, front, cookies)
	remaining := after.entries(t, "entries")
	if len(remaining) != 1 || remaining[0].Name != secondEntry {
		t.Fatalf("desktop entries after uninstalling %s = %+v, want %q only", firstApplication, remaining, secondEntry)
	}
	if hasApplication(after.applications(t, "applications"), firstApplication) {
		t.Fatalf("installed applications = %+v, want the uninstalled application gone", after.applications(t, "applications"))
	}
	if names := eventNames(after.notifications(t, "notifications").Events); names != eventNames(notifications.Events) {
		t.Fatalf("notification history after the uninstall = %v, want it unchanged %v", names, eventNames(notifications.Events))
	}
}

// installEvent writes one recent event about an application, so the
// notification centre has something to read and the account's read state
// something to mark (KOSD-C-64).
func installEvent(t *testing.T, cluster *harness.Cluster, name, application string, seen time.Time) {
	t.Helper()
	stamp := seen.UTC().Format(time.RFC3339)
	events := cluster.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "events"}).
		Namespace(harness.Namespace)
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Event",
		"metadata":   map[string]any{"name": name, "namespace": harness.Namespace},
		"involvedObject": map[string]any{
			"kind": "Application", "namespace": harness.Namespace, "name": application,
		},
		"type": "Normal", "reason": "Started", "message": "the application started",
		"count":              1,
		"firstTimestamp":     stamp,
		"lastTimestamp":      stamp,
		"reportingComponent": "kosd",
	}}
	if _, err := events.Create(t.Context(), object, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create event %s: %v", name, err)
	}
}

// startFront serves the browser front over one composed control plane and logs
// the account in through it.
func startFront(t *testing.T, shell *harness.Shell, username, password string) (*httptest.Server, http.Header) {
	t.Helper()
	upstream, err := url.Parse(shell.Accounts.HTTP.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	server, err := bff.New(bff.Config{Upstream: upstream})
	if err != nil {
		t.Fatalf("build the browser front: %v", err)
	}
	front := httptest.NewServer(server.Handler())
	t.Cleanup(front.Close)
	response := call(t, front, http.MethodPost, "/api/v1/auth/login", "",
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`), "")
	if response.status != http.StatusOK {
		t.Fatalf("login through the front = %d %v", response.status, response.body)
	}
	if _, found := response.decoded["token"]; found {
		t.Fatalf("the login answer carries the control-plane token: %v", response.decoded)
	}
	cookies := http.Header{}
	pairs := make([]string, 0, 2)
	for _, cookie := range response.response.Cookies() {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
	}
	cookies.Set("Cookie", strings.Join(pairs, "; "))
	if csrf := csrfOf(response.response); csrf == "" {
		t.Fatal("login set no CSRF token")
	} else {
		cookies.Set("X-CSRF-Token", csrf)
	}
	return front, cookies
}

// addEntry creates one desktop entry through the page's declared add action.
func addEntry(t *testing.T, front *httptest.Server, cookies http.Header, name, application string) {
	t.Helper()
	method, path := harness.PageAction(t, dock.DesktopPage(), dock.ActionAddEntry, "", harness.Namespace)
	body, err := json.Marshal(map[string]any{
		"apiVersion": dock.APIGroup + "/" + dock.EntryVersion,
		"kind":       dock.EntryKind,
		"metadata":   map[string]any{"name": name, "namespace": harness.Namespace},
		"spec":       map[string]any{"application": application, "visibility": dock.VisibilityEveryone},
	})
	if err != nil {
		t.Fatalf("encode entry %s: %v", name, err)
	}
	response := call(t, front, method, path, cookies.Get("Cookie"), strings.NewReader(string(body)), cookies.Get("X-CSRF-Token"))
	if response.status != http.StatusAccepted {
		t.Fatalf("create entry %s = %d %v", name, response.status, response.body)
	}
}

// uninstall deletes one application and drives the rounds until the object is
// gone, so the desktop reads the state after the uninstall completed.
func uninstall(t *testing.T, cluster *harness.Cluster, name string) {
	t.Helper()
	ctx := context.Background()
	applications := cluster.Direct(t, api.Group, v1beta1.Version, "Application", harness.Namespace)
	if err := applications.Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete application %s: %v", name, err)
	}
	controller := cluster.ApplicationController(t, app.Options{})
	for range 8 {
		if _, err := applications.Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return
		}
		cluster.ApplicationRound(t, controller, name)
	}
	if _, err := applications.Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("application %s survived its uninstall (err = %v)", name, err)
	}
}

// assertSameRead compares one resource read through the front with the same read
// made directly against the control-plane API (KOSD-C-12).
func assertSameRead(t *testing.T, front *httptest.Server, cookies http.Header, shell *harness.Shell, owner string) {
	t.Helper()
	response := call(t, front, http.MethodGet, applicationListPath(), cookies.Get("Cookie"), nil, "")
	if response.status != http.StatusOK {
		t.Fatalf("proxied read = %d %v", response.status, response.body)
	}
	status, direct := shell.Call(t, http.MethodGet, applicationListPath(), nil, owner)
	if status != http.StatusOK {
		t.Fatalf("direct read = %d %v", status, direct)
	}
	proxiedItems := response.decoded["items"].([]any)
	directItems, _ := direct["items"].([]any)
	if len(proxiedItems) != len(directItems) || len(directItems) == 0 {
		t.Fatalf("proxied items = %d, direct items = %d, want the same read", len(proxiedItems), len(directItems))
	}
	for index := range directItems {
		proxied := proxiedItems[index].(map[string]any)
		expected := directItems[index].(map[string]any)
		if !sameJSON(proxied["ref"], expected["ref"]) || !sameJSON(proxied["object"], expected["object"]) {
			t.Fatalf("proxied item %d = %v, want the direct read's %v", index, proxied, expected)
		}
	}
}

// assertNoCredentials checks that no answer the browser receives carries a
// kubeconfig or a service-account token (KOSD-F-9).
func assertNoCredentials(t *testing.T, front *httptest.Server, cookies http.Header) {
	t.Helper()
	paths := []string{"/api/v1/auth/session", "/api/v1/desktop", applicationListPath()}
	for _, path := range paths {
		response := call(t, front, http.MethodGet, path, cookies.Get("Cookie"), nil, "")
		if response.status != http.StatusOK {
			t.Fatalf("read %s = %d %v", path, response.status, response.body)
		}
		forbiddenKeys(t, path, response.decoded)
	}
}

// forbiddenKeys walks one decoded answer looking for credential keys.
func forbiddenKeys(t *testing.T, path string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			lower := strings.ToLower(key)
			for _, forbidden := range []string{"kubeconfig", "serviceaccounttoken", "client-key-data", "client-certificate-data", "token"} {
				if strings.ReplaceAll(lower, "-", "") == strings.ReplaceAll(forbidden, "-", "") {
					t.Fatalf("%s carries a %q field: the browser must never receive cluster credentials (KOSD-F-9)", path, key)
				}
			}
			forbiddenKeys(t, path, nested)
		}
	case []any:
		for _, nested := range typed {
			forbiddenKeys(t, path, nested)
		}
	}
}

// response is one answer the front gave.
type response struct {
	status   int
	body     string
	decoded  map[string]any
	response *http.Response
}

// call sends one request to the front.
func call(t *testing.T, front *httptest.Server, method, path, cookie string, body io.Reader, csrf string) response {
	t.Helper()
	request, err := http.NewRequest(method, front.URL+path, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if cookie != "" {
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
	return response{status: answer.StatusCode, body: string(payload), decoded: decoded, response: answer}
}

// csrfOf reads the CSRF token out of a response.
func csrfOf(answer *http.Response) string {
	for _, cookie := range answer.Cookies() {
		if cookie.Name == "kos_bff_csrf" {
			return cookie.Value
		}
	}
	return ""
}

// readDesktop reads the desktop page through the front.
func readDesktop(t *testing.T, front *httptest.Server, cookies http.Header) desktopPage {
	t.Helper()
	response := call(t, front, http.MethodGet, "/api/v1/desktop", cookies.Get("Cookie"), nil, "")
	if response.status != http.StatusOK {
		t.Fatalf("read the desktop page = %d %v", response.status, response.body)
	}
	var page desktopPage
	if err := json.Unmarshal([]byte(response.body), &page); err != nil {
		t.Fatalf("decode the desktop page: %v", err)
	}
	return page
}

// desktopPage is the rendered page as the front serves it.
type desktopPage struct {
	ID      string       `json:"id"`
	Items   []item       `json:"items"`
	Actions []pageAction `json:"actions"`
}

// pageAction is one action the page declares (DASH-C-22).
type pageAction struct {
	ID           string `json:"id"`
	RequiredRole string `json:"requiredRole"`
	Entry        struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
	} `json:"entry"`
}

// action finds one action the page declares.
func (p desktopPage) action(t *testing.T, id string) pageAction {
	t.Helper()
	for _, action := range p.Actions {
		if action.ID == id {
			return action
		}
	}
	t.Fatalf("the page declares no action %q: %+v", id, p.Actions)
	return pageAction{}
}

// item is one display item the page renders.
type item struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	Store      string          `json:"store"`
	Value      json.RawMessage `json:"value"`
	Action     string          `json:"action"`
	ObservedAt *string         `json:"observedAt"`
	Stale      bool            `json:"stale"`
	Reason     string          `json:"reason"`
}

// item finds one rendered item.
func (p desktopPage) item(id string) item {
	for _, item := range p.Items {
		if item.ID == id {
			return item
		}
	}
	return item{}
}

// entry is one entry the desktop lists.
type entry struct {
	Name        string `json:"name"`
	Application string `json:"application"`
	IsAvailable bool   `json:"isAvailable"`
	Reason      string `json:"reason"`
	Address     string `json:"address"`
}

// entries decodes one item's entries.
func (p desktopPage) entries(t *testing.T, id string) []entry {
	t.Helper()
	var decoded []entry
	if err := json.Unmarshal(p.item(id).Value, &decoded); err != nil {
		t.Fatalf("decode item %q: %v", id, err)
	}
	return decoded
}

// application is one installed application the desktop lists.
type application struct {
	Name  string `json:"name"`
	Phase string `json:"phase"`
}

// applications decodes one item's applications.
func (p desktopPage) applications(t *testing.T, id string) []application {
	t.Helper()
	var decoded []application
	if err := json.Unmarshal(p.item(id).Value, &decoded); err != nil {
		t.Fatalf("decode item %q: %v", id, err)
	}
	return decoded
}

// event is one event the notification centre lists.
type event struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Type      string `json:"type"`
	Read      bool   `json:"read"`
}

// eventRead is the notification centre's value.
type eventRead struct {
	ObservedAt  string  `json:"observedAt"`
	Window      string  `json:"window"`
	ReadThrough string  `json:"readThrough"`
	Unread      int     `json:"unread"`
	Events      []event `json:"events"`
}

// allRead reports whether every listed event carries the read mark.
func allRead(read eventRead) bool {
	for _, event := range read.Events {
		if !event.Read {
			return false
		}
	}
	return true
}

// notifications decodes the notification-centre item.
func (p desktopPage) notifications(t *testing.T, id string) eventRead {
	t.Helper()
	var decoded eventRead
	if err := json.Unmarshal(p.item(id).Value, &decoded); err != nil {
		t.Fatalf("decode item %q: %v", id, err)
	}
	return decoded
}

// eventsOf reads the object route's events directly.
func eventsOf(t *testing.T, shell *harness.Shell, token string) eventRead {
	t.Helper()
	status, body := shell.Call(t, http.MethodGet, "/api/v1/events?limit=100", nil, token)
	if status != http.StatusOK {
		t.Fatalf("read the events = %d %v", status, body)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode the events: %v", err)
	}
	var read eventRead
	if err := json.Unmarshal(payload, &read); err != nil {
		t.Fatalf("decode the events: %v", err)
	}
	return read
}

// eventNames renders the events of one read.
func eventNames(events []event) string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Namespace+"/"+event.Name+"/"+event.Type)
	}
	return strings.Join(names, ",")
}

// hasEntry reports whether the desktop lists one entry.
func hasEntry(entries []entry, name string) bool {
	for _, item := range entries {
		if item.Name == name {
			return true
		}
	}
	return false
}

// hasApplication reports whether the desktop lists one application.
func hasApplication(applications []application, name string) bool {
	for _, item := range applications {
		if item.Name == name {
			return true
		}
	}
	return false
}

// sameJSON compares two decoded JSON values.
func sameJSON(left, right any) bool {
	encodedLeft, _ := json.Marshal(left)
	encodedRight, _ := json.Marshal(right)
	return string(encodedLeft) == string(encodedRight)
}

// applicationListPath is the resource route one read uses.
func applicationListPath() string {
	return "/api/v1/resources/Application?apiVersion=" + v1beta1.GroupVersion.String() + "&namespace=" + harness.Namespace
}
