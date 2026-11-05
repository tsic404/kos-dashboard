package desktop

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/dock"
)

// fixedNow is the read clock every case judges staleness against.
var fixedNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// read is one object the fake control plane returns.
type read struct {
	ref        Ref
	observedAt time.Time
	object     map[string]any
}

// fakeControlPlane answers the resource and event routes the page reads and
// hands every preference read to preferences.
func fakeControlPlane(t *testing.T, entries, applications, endpoints []read, observedAt time.Time, preferences http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/resources/DesktopEntry":
			writeItems(w, entries)
		case "/api/v1/resources/Application":
			writeItems(w, applications)
		case "/api/v1/resources/Endpoint":
			writeItems(w, endpoints)
		case "/api/v1/events":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"observedAt": observedAt,
				"window":     "1h",
				"events": []map[string]any{{
					"name": "media.1", "namespace": "default", "type": "Normal", "reason": "Created",
					"message": "created", "count": 1, "lastSeen": observedAt.Add(-time.Minute),
					"involvedObject": map[string]any{"kind": "Application", "namespace": "default", "name": "media"},
				}},
			})
		case "/api/v1/preferences/kos-dashboard":
			if preferences != nil {
				preferences(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"extension": "kos-dashboard", "items": map[string]string{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// preferencesRead answers one account's domain with the values it holds.
func preferencesRead(items map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if items == nil {
			items = map[string]string{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"extension": "kos-dashboard", "items": items})
	}
}

// preferencesRefused answers the read with one control-plane refusal.
func preferencesRefused(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// writeItems writes one page of projections.
func writeItems(w http.ResponseWriter, items []read) {
	projections := make([]map[string]any, 0, len(items))
	for _, item := range items {
		projections = append(projections, map[string]any{
			"ref":        item.ref,
			"observedAt": item.observedAt,
			"stale":      false,
			"object":     item.object,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"items": projections})
}

// object builds one stored object.
func object(namespace, name string, spec, status map[string]any) map[string]any {
	built := map[string]any{"metadata": map[string]any{"name": name, "namespace": namespace}}
	if spec != nil {
		built["spec"] = spec
	}
	if status != nil {
		built["status"] = status
	}
	return built
}

// newClient builds a reader over one fake control plane.
func newClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse the API URL: %v", err)
	}
	return New(Config{BaseURL: base, Token: "session-token", Now: func() time.Time { return fixedNow }})
}

// itemOf finds one rendered item.
func itemOf(t *testing.T, page Page, id string) Item {
	t.Helper()
	for _, item := range page.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("the page carries no item %q: %+v", id, page.Items)
	return Item{}
}

// entriesOf decodes the entries item.
func entriesOf(t *testing.T, item Item) []Entry {
	t.Helper()
	payload, err := json.Marshal(item.Value)
	if err != nil {
		t.Fatalf("encode the entries: %v", err)
	}
	var entries []Entry
	if err := json.Unmarshal(payload, &entries); err != nil {
		t.Fatalf("decode the entries: %v", err)
	}
	return entries
}

// applicationsOf decodes the applications item.
func applicationsOf(t *testing.T, item Item) []Application {
	t.Helper()
	payload, err := json.Marshal(item.Value)
	if err != nil {
		t.Fatalf("encode the applications: %v", err)
	}
	var applications []Application
	if err := json.Unmarshal(payload, &applications); err != nil {
		t.Fatalf("decode the applications: %v", err)
	}
	return applications
}

// unreadOf reads the stated unread count.
func unreadOf(t *testing.T, centre NotificationCentre) int {
	t.Helper()
	if centre.Unread == nil {
		t.Fatal("the centre states no unread count")
	}
	return *centre.Unread
}

// readOf reads one event's read mark.
func readOf(t *testing.T, notification Notification) bool {
	t.Helper()
	if notification.Read == nil {
		t.Fatal("the event carries no read mark")
	}
	return *notification.Read
}

// TestPageListsTheFourSources checks the desktop page DASH-C-52 declares: the
// entries, the installed applications, the browser-local recent list and the
// notification centre, each with its source (DASH-AC-1).
func TestPageListsTheFourSources(t *testing.T) {
	entries := []read{
		{ref: Ref{Kind: "DesktopEntry", Namespace: "default", Name: "media-desktop"}, observedAt: fixedNow.Add(-time.Minute),
			object: object("default", "media-desktop", map[string]any{
				"application": "media", "displayName": "媒体库", "launch": "media-web", "visibility": "everyone", "order": 1,
			}, nil)},
		{ref: Ref{Kind: "DesktopEntry", Namespace: "default", Name: "broken-desktop"}, observedAt: fixedNow.Add(-time.Minute),
			object: object("default", "broken-desktop", map[string]any{"application": "broken", "order": 2}, nil)},
		{ref: Ref{Kind: "DesktopEntry", Namespace: "default", Name: "gone-desktop"}, observedAt: fixedNow.Add(-time.Minute),
			object: object("default", "gone-desktop", map[string]any{"application": "gone", "order": 3}, nil)},
	}
	applications := []read{
		{ref: Ref{Kind: "Application", Namespace: "default", Name: "media"}, observedAt: fixedNow.Add(-time.Minute),
			object: object("default", "media", nil, map[string]any{"phase": "Ready", "health": map[string]any{"state": "Healthy"}})},
		{ref: Ref{Kind: "Application", Namespace: "default", Name: "broken"}, observedAt: fixedNow.Add(-time.Minute),
			object: object("default", "broken", nil, map[string]any{"phase": "Failed", "health": map[string]any{"state": "Unavailable"}})},
	}
	endpoints := []read{
		{ref: Ref{Kind: "Endpoint", Namespace: "default", Name: "media-web"}, observedAt: fixedNow.Add(-time.Minute),
			object: object("default", "media-web", map[string]any{
				"kind": "http.host", "targetApp": "media", "host": "media.example.test",
				"tls": map[string]any{"issuer": "kos"},
			}, nil)},
	}
	server := fakeControlPlane(t, entries, applications, endpoints, fixedNow.Add(-time.Minute), preferencesRead(nil))
	page, err := newClient(t, server).Page(t.Context())
	if err != nil {
		t.Fatalf("read the desktop page: %v", err)
	}

	if page.ID != PageID || page.Title == "" || page.ReadOnly {
		t.Fatalf("page = %+v, want the writable desktop page", page)
	}
	if len(page.Items) != 4 {
		t.Fatalf("items = %+v, want the four DASH-C-52 declares", page.Items)
	}
	if sources := strings.Join(page.Sources, ","); sources != SourceObject+","+SourceLocal {
		t.Fatalf("sources = %q, want object and local", sources)
	}
	for _, id := range []string{ItemEntries, ItemApplications, ItemRecent, ItemNotifications} {
		item := itemOf(t, page, id)
		if item.Source == "" {
			t.Fatalf("item %q carries no source (KOSD-C-53)", id)
		}
	}
	recent := itemOf(t, page, ItemRecent)
	if recent.Source != SourceLocal || recent.Store != RecentStore || recent.Value != nil {
		t.Fatalf("recent item = %+v, want a browser-local entry", recent)
	}
	if recent.ObservedAt != nil {
		t.Fatal("a browser-local item claims a control-plane observation")
	}
	if itemOf(t, page, ItemApplications).Reason == "" {
		t.Fatal("a read-only item carries no reason (KOSD-C-53)")
	}
	if itemOf(t, page, ItemNotifications).Action != dock.ActionMarkNotificationsRead {
		t.Fatal("the notification centre carries no read write (DASH-C-27)")
	}
	entriesItem := itemOf(t, page, ItemEntries)
	if entriesItem.Action != "remove-entry" || entriesItem.Entry == nil {
		t.Fatalf("entries item = %+v, want the declared remove action", entriesItem)
	}

	rendered := entriesOf(t, entriesItem)
	if len(rendered) != 2 {
		t.Fatalf("entries = %+v, want the two installed applications", rendered)
	}
	if rendered[0].Name != "media-desktop" || !rendered[0].IsAvailable {
		t.Fatalf("first entry = %+v, want the openable media entry", rendered[0])
	}
	if rendered[0].Address != "https://media.example.test/" || rendered[0].Endpoint != "media-web" {
		t.Fatalf("media entry launch = %+v, want the endpoint's address", rendered[0])
	}
	if rendered[1].Name != "broken-desktop" || rendered[1].IsAvailable || rendered[1].Reason == "" {
		t.Fatalf("second entry = %+v, want the unusable application kept with its reason", rendered[1])
	}
}

// TestUninstalledApplicationLeavesTheDesktop checks that an entry whose
// application is no longer installed is not listed, while the notification
// history is unaffected (DASH-AC-1).
func TestUninstalledApplicationLeavesTheDesktop(t *testing.T) {
	entries := []read{
		{ref: Ref{Kind: "DesktopEntry", Namespace: "default", Name: "media-desktop"}, observedAt: fixedNow,
			object: object("default", "media-desktop", map[string]any{"application": "media"}, nil)},
	}
	applications := []read{
		{ref: Ref{Kind: "Application", Namespace: "default", Name: "other"}, observedAt: fixedNow,
			object: object("default", "other", nil, map[string]any{"phase": "Ready"})},
	}
	server := fakeControlPlane(t, entries, applications, nil, fixedNow.Add(-time.Minute), preferencesRead(nil))
	page, err := newClient(t, server).Page(t.Context())
	if err != nil {
		t.Fatalf("read the desktop page: %v", err)
	}
	if rendered := entriesOf(t, itemOf(t, page, ItemEntries)); len(rendered) != 0 {
		t.Fatalf("entries = %+v, want an uninstalled application's entry left out", rendered)
	}
	notifications := itemOf(t, page, ItemNotifications)
	centre, ok := notifications.Value.(NotificationCentre)
	if !ok {
		t.Fatalf("notifications = %+v, want the notification centre", notifications.Value)
	}
	if len(centre.Events) != 1 {
		t.Fatalf("notifications events = %v, want the history kept", centre.Events)
	}
}

// TestNotificationReadStateFollowsTheAccount checks the notification centre
// carries the account's read watermark and the write that moves it: an event
// seen at or before the watermark reads as read, the write stores the moment
// this read observed the events, and each action names the role that may submit
// it (DASH-C-27, DASH-C-22).
func TestNotificationReadStateFollowsTheAccount(t *testing.T) {
	observedAt := fixedNow.Add(-time.Minute)
	readAt := observedAt.Add(-time.Minute)
	server := fakeControlPlane(t, nil, nil, nil, observedAt, preferencesRead(map[string]string{
		dock.PreferenceRead: encodeReadState(readAt),
	}))
	page, err := newClient(t, server).Page(t.Context())
	if err != nil {
		t.Fatalf("read the desktop page: %v", err)
	}

	item := itemOf(t, page, ItemNotifications)
	centre, ok := item.Value.(NotificationCentre)
	if !ok {
		t.Fatalf("notifications = %+v, want the notification centre", item.Value)
	}
	if len(centre.Events) != 1 || !readOf(t, centre.Events[0]) || unreadOf(t, centre) != 0 {
		t.Fatalf("notifications = %+v, want the stored watermark to cover the event", centre)
	}
	if centre.ReadThrough == nil || !centre.ReadThrough.Equal(readAt) {
		t.Fatalf("readThrough = %v, want %v", centre.ReadThrough, readAt)
	}

	if item.Action != dock.ActionMarkNotificationsRead || item.Entry == nil {
		t.Fatalf("notification item = %+v, want the declared read write", item)
	}
	if item.Reason != "" {
		t.Fatalf("notification item = %+v, want no read-only reason while the state is readable", item)
	}
	if item.Entry.Method != http.MethodPut || item.Entry.Path != "/api/v1/preferences/kos-dashboard/read" {
		t.Fatalf("read write = %+v, want the preference route", item.Entry)
	}
	body, ok := item.Entry.Body.(map[string]any)
	if !ok {
		t.Fatalf("read write body = %v, want the value the control plane stores", item.Entry.Body)
	}
	if body["value"] != encodeReadState(observedAt) {
		t.Fatalf("read write value = %v, want the observation %v", body["value"], observedAt)
	}

	roles := map[string]string{}
	for _, action := range page.Actions {
		roles[action.ID] = action.RequiredRole
	}
	if roles[dock.ActionMarkNotificationsRead] != "Member" {
		t.Fatalf("read write role = %q, want Member: a Guest cannot write preferences", roles[dock.ActionMarkNotificationsRead])
	}
	if roles[dock.ActionRemoveEntry] != "Owner" {
		t.Fatalf("remove entry role = %q, want Owner: only the global Owner writes entries", roles[dock.ActionRemoveEntry])
	}
}

// TestNotificationWithoutAStoredWatermarkIsUnread checks that an account which
// marked nothing read sees every listed event unread (DASH-C-27).
func TestNotificationWithoutAStoredWatermarkIsUnread(t *testing.T) {
	observedAt := fixedNow.Add(-time.Minute)
	server := fakeControlPlane(t, nil, nil, nil, observedAt, preferencesRead(nil))
	page, err := newClient(t, server).Page(t.Context())
	if err != nil {
		t.Fatalf("read the desktop page: %v", err)
	}
	centre, ok := itemOf(t, page, ItemNotifications).Value.(NotificationCentre)
	if !ok {
		t.Fatalf("notifications = %+v, want the notification centre", itemOf(t, page, ItemNotifications).Value)
	}
	if unreadOf(t, centre) != 1 || len(centre.Events) != 1 || readOf(t, centre.Events[0]) {
		t.Fatalf("notifications = %+v, want the one event unread", centre)
	}
	if centre.ReadThrough != nil {
		t.Fatalf("readThrough = %v, want no watermark", centre.ReadThrough)
	}
}

// TestUnreadableReadStateCountsAsNothingRead checks that a stored value this
// product cannot read leaves the events unread instead of failing the page
// (DASH-C-27).
func TestUnreadableReadStateCountsAsNothingRead(t *testing.T) {
	cases := map[string]string{
		"not json":            "not-a-value",
		"a later version":     `{"schemaVersion":2,"readThrough":"2026-03-01T11:59:00Z"}`,
		"a broken timestamp":  `{"schemaVersion":1,"readThrough":"yesterday"}`,
		"a missing watermark": `{"schemaVersion":1}`,
	}
	for name, stored := range cases {
		t.Run(name, func(t *testing.T) {
			observedAt := fixedNow.Add(-time.Minute)
			server := fakeControlPlane(t, nil, nil, nil, observedAt, preferencesRead(map[string]string{dock.PreferenceRead: stored}))
			page, err := newClient(t, server).Page(t.Context())
			if err != nil {
				t.Fatalf("read the desktop page: %v", err)
			}
			centre, ok := itemOf(t, page, ItemNotifications).Value.(NotificationCentre)
			if !ok {
				t.Fatalf("notifications = %+v, want the notification centre", itemOf(t, page, ItemNotifications).Value)
			}
			if unreadOf(t, centre) != 1 || centre.ReadThrough != nil {
				t.Fatalf("notifications = %+v, want nothing read", centre)
			}
		})
	}
}

// TestReadStateFailureDegradesTheNotificationItem checks that an unavailable
// read state costs the page only that state: the control plane still serves the
// four object sources, so the page renders with the notification centre marked
// unavailable, its events listed without read marks, and no write entry that
// would be refused (KOSD-C-61, DASH-C-27).
func TestReadStateFailureDegradesTheNotificationItem(t *testing.T) {
	entries := []read{
		{ref: Ref{Kind: "DesktopEntry", Namespace: "default", Name: "media-desktop"}, observedAt: fixedNow,
			object: object("default", "media-desktop", map[string]any{"application": "media"}, nil)},
	}
	applications := []read{
		{ref: Ref{Kind: "Application", Namespace: "default", Name: "media"}, observedAt: fixedNow,
			object: object("default", "media", nil, map[string]any{"phase": "Ready"})},
	}
	cases := []struct {
		name    string
		status  int
		body    string
		message string
	}{
		{
			name:    "the control-plane database is unavailable",
			status:  http.StatusServiceUnavailable,
			body:    `{"error":{"code":"database_unavailable","message":"控制面数据库暂不可用，请稍后重试","stage":"apply","retryable":true,"requiresUserAction":false,"sideEffects":"none"}}`,
			message: "控制面数据库暂不可用，请稍后重试",
		},
		{
			name:    "no active extension declares the domain",
			status:  http.StatusNotFound,
			body:    `{"error":{"code":"not_found","message":"没有生效的扩展声明这个偏好域","stage":"validate","retryable":false,"requiresUserAction":true,"sideEffects":"none"}}`,
			message: "没有生效的扩展声明这个偏好域",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observedAt := fixedNow.Add(-time.Minute)
			server := fakeControlPlane(t, entries, applications, nil, observedAt,
				preferencesRefused(testCase.status, testCase.body))
			page, err := newClient(t, server).Page(t.Context())
			if err != nil {
				t.Fatalf("the page failed on an auxiliary read: %v", err)
			}
			if len(page.Items) != 4 {
				t.Fatalf("items = %+v, want the four DASH-C-52 declares", page.Items)
			}
			if rendered := entriesOf(t, itemOf(t, page, ItemEntries)); len(rendered) != 1 ||
				rendered[0].Name != "media-desktop" {
				t.Fatalf("entries = %+v, want the entry rendered beside an unavailable read state", rendered)
			}
			if rendered := applicationsOf(t, itemOf(t, page, ItemApplications)); len(rendered) != 1 ||
				rendered[0].Name != "media" {
				t.Fatalf("applications = %+v, want the application rendered beside an unavailable read state", rendered)
			}

			item := itemOf(t, page, ItemNotifications)
			centre, ok := item.Value.(NotificationCentre)
			if !ok {
				t.Fatalf("notifications = %+v, want the notification centre", item.Value)
			}
			if !centre.ReadStateUnavailable || centre.ReadStateReason != testCase.message {
				t.Fatalf("notification centre = %+v, want the read state marked unavailable with %q",
					centre, testCase.message)
			}
			if centre.ReadThrough != nil || centre.Unread != nil {
				t.Fatalf("notification centre = %+v, want no watermark and no unread count", centre)
			}
			if len(centre.Events) != 1 || centre.Events[0].Read != nil {
				t.Fatalf("notification centre = %+v, want the event listed without a read mark", centre)
			}
			if item.Action != "" || item.Entry != nil {
				t.Fatalf("notification item = %+v, want no write entry while the read state is unavailable", item)
			}
			if item.Reason != "已读状态不可用："+testCase.message {
				t.Fatalf("notification item reason = %q, want the unavailable state stated", item.Reason)
			}
			for _, action := range page.Actions {
				if action.ID == dock.ActionMarkNotificationsRead {
					t.Fatalf("the page offers %q although the write would be refused", action.ID)
				}
			}
		})
	}
}

// TestStaleObservationMarksTheItem checks that an observation beyond the two
// minute bound marks the item stale (KOSD-C-13).
func TestStaleObservationMarksTheItem(t *testing.T) {
	old := fixedNow.Add(-3 * time.Minute)
	entries := []read{
		{ref: Ref{Kind: "DesktopEntry", Namespace: "default", Name: "media-desktop"}, observedAt: fixedNow,
			object: object("default", "media-desktop", map[string]any{"application": "media"}, nil)},
	}
	applications := []read{
		{ref: Ref{Kind: "Application", Namespace: "default", Name: "media"}, observedAt: old,
			object: object("default", "media", nil, map[string]any{"phase": "Ready"})},
	}
	server := fakeControlPlane(t, entries, applications, nil, fixedNow.Add(-time.Minute), preferencesRead(nil))
	page, err := newClient(t, server).Page(t.Context())
	if err != nil {
		t.Fatalf("read the desktop page: %v", err)
	}
	entriesItem := itemOf(t, page, ItemEntries)
	if !entriesItem.Stale || entriesItem.ObservedAt == nil || !entriesItem.ObservedAt.Equal(old) {
		t.Fatalf("entries item = %+v, want the oldest observation and a stale mark", entriesItem)
	}
	if !page.Stale {
		t.Fatal("the page is not marked stale with a stale item")
	}
	if itemOf(t, page, ItemNotifications).Stale {
		t.Fatal("a fresh event read is marked stale")
	}
}

// TestRefusedReadKeepsTheControlPlaneEnvelope checks that a refused source
// fails the page with the API's own refusal, so the front and the API refuse
// the same read the same way (KOSD-C-12).
func TestRefusedReadKeepsTheControlPlaneEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"capability_missing","message":"class not served","stage":"capability","retryable":false,"requiresUserAction":false,"sideEffects":"none"}}`))
	}))
	t.Cleanup(server.Close)
	_, err := newClient(t, server).Page(t.Context())
	failure, ok := err.(*Failure)
	if !ok || failure.Status != http.StatusUnprocessableEntity {
		t.Fatalf("read error = %v, want the API's refusal", err)
	}
	envelope := map[string]any{}
	if err := json.Unmarshal(failure.Body, &envelope); err != nil {
		t.Fatalf("decode the envelope: %v", err)
	}
	failureBody, _ := envelope["error"].(map[string]any)
	if failureBody["code"] != "capability_missing" {
		t.Fatalf("envelope = %v, want the API's code", envelope)
	}
}
