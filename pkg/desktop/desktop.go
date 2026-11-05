// Package desktop assembles the desktop page DASH-C-52 describes: the desktop
// entries, the installed applications, the recent list the browser holds and the
// notification centre's recent events.
//
// Every server-side item is read through the control-plane API (KOSD-C-12), so
// this front holds no cluster credential of its own (KOSD-F-9). Entries are
// resolved against the application they name, which is what keeps an entry on
// its application when a downstream object is renamed (DASH-C-30); an entry
// whose application is no longer installed is not listed at all, while an
// installed but unusable application keeps its entry and carries the reason
// (DASH-AC-1).
package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsic404/kosd/api/v1beta1"

	"github.com/tsic404/kos-dashboard/pkg/dock"
)

const (
	// StaleThreshold is the one freshness bound every read is judged by
	// (KOSD-C-13): an observation older than two minutes is stale.
	StaleThreshold = 2 * time.Minute
	// defaultEventsLimit bounds the notification-centre read; the control plane
	// fixes the window at the most recent hour.
	defaultEventsLimit = 100
	// pageSize is the largest page the resource API serves.
	pageSize = 500
	// maxPages bounds one list so a wedged cursor cannot spin.
	maxPages = 20
	// maxReadBytes bounds one API response body.
	maxReadBytes = 8 << 20
)

// The page identity and the item identities the desktop page declares
// (DASH-C-52). They are the same identities the extension declaration carries.
const (
	// PageID is the desktop page identity.
	PageID = dock.PageID
	// ItemEntries is the desktop entries item.
	ItemEntries = "entries"
	// ItemApplications is the installed applications item.
	ItemApplications = "applications"
	// ItemRecent is the browser-local recent list.
	ItemRecent = "recent"
	// ItemNotifications is the notification centre.
	ItemNotifications = "notifications"
	// RecentStore is the browser storage key the recent list lives in.
	RecentStore = "desktop.recent"
)

// The source types a page item may carry (KOSD-C-53).
const (
	// SourceObject is a cluster object or its projection.
	SourceObject = "object"
	// SourceLocal is browser storage.
	SourceLocal = "local"
)

// Class identifies one served object class.
type Class struct {
	// APIVersion is the served group and version.
	APIVersion string
	// Kind is the object kind.
	Kind string
}

// Config wires the page reader to the control-plane API.
type Config struct {
	// BaseURL is the control-plane API base URL; required.
	BaseURL *url.URL
	// Token is the caller's control-plane session; every read runs as that
	// session, so visibility and role rules apply exactly as they do directly
	// (KOSD-C-59).
	Token string
	// HTTP is the client every read uses; defaults to a bounded one.
	HTTP *http.Client
	// Now supplies the clock the stale marker compares against; defaults to
	// time.Now.
	Now func() time.Time
	// EventsLimit bounds the notification-centre read; zero reads the default.
	EventsLimit int
}

// Client reads the desktop page through the control-plane API.
type Client struct {
	baseURL     *url.URL
	token       string
	client      *http.Client
	now         func() time.Time
	eventsLimit int
}

// New builds the desktop reader.
func New(config Config) *Client {
	client := &Client{
		baseURL:     config.BaseURL,
		token:       config.Token,
		client:      config.HTTP,
		now:         config.Now,
		eventsLimit: config.EventsLimit,
	}
	if client.client == nil {
		client.client = &http.Client{Timeout: 30 * time.Second}
	}
	if client.now == nil {
		client.now = time.Now
	}
	if client.eventsLimit <= 0 {
		client.eventsLimit = defaultEventsLimit
	}
	return client
}

// Ref identifies one object in the projection.
type Ref struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Event is one cluster event as the notification centre shows it (KOSD-C-64).
// The shape matches the control plane's event read, so the page and the events
// route report the same events.
type Event struct {
	Name           string     `json:"name"`
	Namespace      string     `json:"namespace,omitempty"`
	Type           string     `json:"type"`
	Reason         string     `json:"reason"`
	Message        string     `json:"message"`
	Count          int64      `json:"count"`
	FirstSeen      *time.Time `json:"firstSeen,omitempty"`
	LastSeen       time.Time  `json:"lastSeen"`
	InvolvedObject Ref        `json:"involvedObject"`
}

// Identity names one event occurrence, so the stream can tell a repeat from an
// event it already delivered.
func (e Event) Identity() string {
	return fmt.Sprintf("%s/%s/%s/%d", e.Namespace, e.Name, e.LastSeen.UTC().Format(time.RFC3339Nano), e.Count)
}

// EventRead is one recent-event read (KOSD-C-64): the window, the events and
// the moment the read observed them.
type EventRead struct {
	ObservedAt time.Time `json:"observedAt"`
	Window     string    `json:"window"`
	Events     []Event   `json:"events"`
}

// Notification is one event with the account's read state (DASH-C-27): an
// event is read once it was seen at or before the account's watermark, so a
// later occurrence of the same event reads as unread again. Read is absent when
// the account's read state could not be read.
type Notification struct {
	Event
	Read *bool `json:"read,omitempty"`
}

// NotificationCentre is the notification item's value: the recent events with
// their read state and the watermark the account last marked read. It carries
// the event read unchanged, so the page and the object route report the same
// events (KOSD-C-64, DASH-C-52).
//
// The read state is auxiliary: when the control plane cannot serve it — its
// database is down (KOSD-C-61) or no active extension declares the domain — the
// events still render, ReadThrough and Unread are absent, and
// ReadStateUnavailable says why (DASH-C-27).
type NotificationCentre struct {
	ObservedAt time.Time      `json:"observedAt"`
	Window     string         `json:"window"`
	Events     []Notification `json:"events"`
	// ReadThrough is the watermark the account last marked read; absent means
	// the account marked nothing read yet, or the read state is unavailable.
	ReadThrough *time.Time `json:"readThrough,omitempty"`
	// Unread counts the listed events the watermark does not cover; absent
	// when the read state is unavailable.
	Unread *int `json:"unread,omitempty"`
	// ReadStateUnavailable marks that the account's read state could not be
	// read: the events are listed without read marks, and ReadStateReason
	// carries the refusal.
	ReadStateUnavailable bool `json:"readStateUnavailable,omitempty"`
	// ReadStateReason is the control plane's own refusal.
	ReadStateReason string `json:"readStateReason,omitempty"`
}

// Application is one installed application as the desktop page lists it.
type Application struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Phase     string `json:"phase"`
	Health    string `json:"health"`
}

// Entry is one desktop entry resolved against its application (DASH-C-30).
type Entry struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
	Application string `json:"application"`
	DisplayName string `json:"displayName"`
	Icon        string `json:"icon,omitempty"`
	Visibility  string `json:"visibility"`
	Order       int64  `json:"order"`
	Source      string `json:"source,omitempty"`
	IsAvailable bool   `json:"isAvailable"`
	Reason      string `json:"reason,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Address     string `json:"address,omitempty"`
}

// ActionEntry is one control-plane write a page action submits (KOSD-C-22).
// Body is the request body the shell sends with the write; absent sends none.
type ActionEntry struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   any    `json:"body,omitempty"`
}

// Action is one executable page action as the shell offers it. RequiredRole
// narrows the action to the roles that may submit it (DASH-C-22, KOSD-C-59).
type Action struct {
	ID           string      `json:"id"`
	RequiredRole string      `json:"requiredRole,omitempty"`
	Entry        ActionEntry `json:"entry"`
}

// Item is one display item with its current value and source (KOSD-C-53).
type Item struct {
	ID         string       `json:"id"`
	Label      string       `json:"label"`
	Source     string       `json:"source"`
	Value      any          `json:"value"`
	Store      string       `json:"store,omitempty"`
	ObservedAt *time.Time   `json:"observedAt,omitempty"`
	Stale      bool         `json:"stale"`
	Action     string       `json:"action,omitempty"`
	Entry      *ActionEntry `json:"entry,omitempty"`
	Reason     string       `json:"reason,omitempty"`
}

// Page is the desktop page with its items rendered.
type Page struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Location   string    `json:"location"`
	ReadOnly   bool      `json:"readOnly"`
	Sources    []string  `json:"sources"`
	Items      []Item    `json:"items"`
	Actions    []Action  `json:"actions,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
	Stale      bool      `json:"stale"`
}

// Failure is a read the control-plane API refused. The body is the envelope the
// API wrote, so the front answers exactly what a direct call answers
// (KOSD-C-12).
type Failure struct {
	// Status is the HTTP status the API answered.
	Status int
	// Body is the envelope body.
	Body []byte
}

// Error renders the refusal.
func (f *Failure) Error() string {
	return fmt.Sprintf("control-plane API answered %d", f.Status)
}

// reason reads the refusal's own message, so a degraded item explains the
// missing state the way the control plane does.
func (f *Failure) reason() string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(f.Body, &envelope); err == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	return fmt.Sprintf("控制面回答 %d", f.Status)
}

// Page reads every source of the desktop page and renders it: the entries, the
// applications, the endpoints, the recent events and the account's notification
// read state (DASH-C-52, DASH-C-27). A source the control plane cannot serve
// fails the whole page rather than showing a hole: a page is either its
// declared projection or a refusal (KOSD-C-53). The read state is the one
// auxiliary source: a control plane whose preference store is unavailable
// (KOSD-C-61) still serves every object read, so the page renders with that
// state marked unavailable instead of failing whole.
func (c *Client) Page(ctx context.Context) (Page, error) {
	entries, failure := c.list(ctx, dock.EntryKind, dock.APIGroup+"/"+dock.EntryVersion)
	if failure != nil {
		return Page{}, failure
	}
	applications, failure := c.list(ctx, "Application", v1beta1.GroupVersion.String())
	if failure != nil {
		return Page{}, failure
	}
	endpoints, failure := c.list(ctx, "Endpoint", v1beta1.GroupVersion.String())
	if failure != nil {
		return Page{}, failure
	}
	events, err := c.Events(ctx)
	if err != nil {
		return Page{}, err
	}
	// The account's read state is auxiliary: a control plane that cannot serve
	// it — a down database (KOSD-C-61) or a domain no active extension declares
	// — still answers every object read, so the page renders with the read
	// state marked unavailable instead of failing whole (DASH-C-27).
	watermark, readStateFailure := c.readState(ctx)
	readStateAvailable := readStateFailure == nil

	now := c.now()
	// The mark-read write stores the moment this read observed the events, so a
	// click covers exactly what the page showed and no event it did not.
	mark := events.ObservedAt
	if mark.IsZero() {
		mark = now
	}
	applicationsByName := indexApplications(applications)
	renderedEntries := resolveEntries(entries, applicationsByName, endpoints)
	renderedApplications := renderApplications(applications)

	page := Page{
		ID:       PageID,
		Title:    "桌面",
		Location: "desktop",
		Sources:  []string{SourceObject, SourceLocal},
		Actions:  actionsOf(mark, readStateAvailable),
	}
	entryItem := Item{ID: ItemEntries, Label: "桌面条目", Source: SourceObject, Value: renderedEntries,
		Action: dock.ActionRemoveEntry}
	if entry, found := actionEntry(page.Actions, dock.ActionRemoveEntry); found {
		entryItem.Entry = &entry
	}
	entryItem.ObservedAt, entryItem.Stale = observe(now, entries, applications, endpoints)
	applicationItem := Item{ID: ItemApplications, Label: "已安装应用", Source: SourceObject,
		Value: renderedApplications, Reason: "桌面只打开应用，不修改应用"}
	applicationItem.ObservedAt, applicationItem.Stale = observe(now, applications)
	recentItem := Item{ID: ItemRecent, Label: "最近使用", Source: SourceLocal, Store: RecentStore,
		Reason: "最近使用只存在于浏览器"}
	centre := renderNotifications(events, watermark, readStateFailure)
	notificationItem := Item{ID: ItemNotifications, Label: "通知中心", Source: SourceObject, Value: centre}
	if readStateAvailable {
		// A read state the page could not read would refuse a write too, so the
		// entry is not offered while the state is unavailable (DASH-C-22).
		notificationItem.Action = dock.ActionMarkNotificationsRead
		if entry, found := actionEntry(page.Actions, dock.ActionMarkNotificationsRead); found {
			notificationItem.Entry = &entry
		}
	} else {
		// The item is read-only in this state; the reason says why, so the shell
		// states it without knowing the projection's internals (KOSD-C-53).
		notificationItem.Reason = "已读状态不可用：" + centre.ReadStateReason
	}
	observedAt := events.ObservedAt.UTC()
	notificationItem.ObservedAt = &observedAt
	notificationItem.Stale = now.Sub(events.ObservedAt) > StaleThreshold

	page.Items = []Item{entryItem, applicationItem, recentItem, notificationItem}
	page.ObservedAt, page.Stale = pageObservation(page.Items)
	return page, nil
}

// Events reads the recent events the notification centre shows (KOSD-C-64).
func (c *Client) Events(ctx context.Context) (EventRead, error) {
	query := url.Values{}
	query.Set("limit", fmt.Sprintf("%d", c.eventsLimit))
	payload, failure := c.get(ctx, "/api/v1/events", query)
	if failure != nil {
		return EventRead{}, failure
	}
	var read EventRead
	if err := json.Unmarshal(payload, &read); err != nil {
		return EventRead{}, fmt.Errorf("desktop: decode the event read: %w", err)
	}
	return read, nil
}

// preferenceRead is one account's preferences inside the extension's domain
// (DASH-C-27).
type preferenceRead struct {
	Items map[string]string `json:"items"`
}

// readState reads the account's notification read watermark. The preference
// belongs to the account, not to the browser, so the same account reads the
// same state wherever it signs in (DASH-C-27).
func (c *Client) readState(ctx context.Context) (time.Time, *Failure) {
	payload, failure := c.get(ctx, "/api/v1/preferences/"+dock.Identifier, nil)
	if failure != nil {
		return time.Time{}, failure
	}
	var read preferenceRead
	if err := json.Unmarshal(payload, &read); err != nil {
		return time.Time{}, &Failure{Status: http.StatusBadGateway}
	}
	return decodeReadState(read.Items[dock.PreferenceRead]), nil
}

// ReadStateVersion is the schema version the notification read preference is
// written with (DASH-C-27).
const ReadStateVersion = 1

// readState is the value the notification read key stores: the watermark the
// account marked read. A value this product cannot read is treated as no state
// and left in place until the account writes again (DASH-C-27).
type readState struct {
	SchemaVersion int    `json:"schemaVersion"`
	ReadThrough   string `json:"readThrough"`
}

// decodeReadState reads the watermark out of one stored value; an unreadable or
// unsupported value reads as nothing read.
func decodeReadState(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	var state readState
	if err := json.Unmarshal([]byte(value), &state); err != nil || state.SchemaVersion != ReadStateVersion {
		return time.Time{}
	}
	watermark, err := time.Parse(time.RFC3339Nano, state.ReadThrough)
	if err != nil {
		return time.Time{}
	}
	return watermark
}

// encodeReadState renders the value the mark-read action writes.
func encodeReadState(watermark time.Time) string {
	payload, err := json.Marshal(readState{
		SchemaVersion: ReadStateVersion,
		ReadThrough:   watermark.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return ""
	}
	return string(payload)
}

// projection is one object as the read path returns it (KOSD-C-13).
type projection struct {
	Ref        Ref            `json:"ref"`
	ObservedAt time.Time      `json:"observedAt"`
	Stale      bool           `json:"stale"`
	Object     map[string]any `json:"object"`
}

// listResponse is one page of the resource API.
type listResponse struct {
	Items    []projection `json:"items"`
	Continue string       `json:"continue,omitempty"`
}

// list reads every object of one class, following the cursor the API returns.
func (c *Client) list(ctx context.Context, kind, apiVersion string) ([]projection, *Failure) {
	collected := make([]projection, 0)
	cursor := ""
	for range maxPages {
		query := url.Values{}
		query.Set("apiVersion", apiVersion)
		query.Set("limit", fmt.Sprintf("%d", pageSize))
		if cursor != "" {
			query.Set("continue", cursor)
		}
		payload, failure := c.get(ctx, "/api/v1/resources/"+kind, query)
		if failure != nil {
			return nil, failure
		}
		var read listResponse
		if err := json.Unmarshal(payload, &read); err != nil {
			return nil, &Failure{Status: http.StatusBadGateway, Body: nil}
		}
		collected = append(collected, read.Items...)
		if read.Continue == "" {
			return collected, nil
		}
		cursor = read.Continue
	}
	return collected, nil
}

// get performs one authenticated read against the control-plane API.
func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, *Failure) {
	if c.baseURL == nil {
		return nil, &Failure{Status: http.StatusBadGateway}
	}
	target := *c.baseURL
	target.Path = path
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, &Failure{Status: http.StatusBadGateway}
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, &Failure{Status: http.StatusBadGateway}
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxReadBytes))
	if err != nil {
		return nil, &Failure{Status: http.StatusBadGateway}
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, &Failure{Status: response.StatusCode, Body: payload}
	}
	return payload, nil
}

// indexApplications maps every listed application by namespace and name.
func indexApplications(applications []projection) map[string]*unstructured.Unstructured {
	index := make(map[string]*unstructured.Unstructured, len(applications))
	for _, item := range applications {
		index[keyOf(item.Ref.Namespace, item.Ref.Name)] = objectOf(item.Object)
	}
	return index
}

// resolveEntries resolves every entry against its application and the
// endpoints of its namespace. An entry whose application is no longer
// installed is left out: the desktop lists entries of installed applications
// (DASH-AC-1).
func resolveEntries(entries []projection, applications map[string]*unstructured.Unstructured, endpoints []projection) []Entry {
	byNamespace := make(map[string][]*unstructured.Unstructured)
	for _, item := range endpoints {
		byNamespace[item.Ref.Namespace] = append(byNamespace[item.Ref.Namespace], objectOf(item.Object))
	}
	resolved := make([]Entry, 0, len(entries))
	for _, item := range entries {
		entry := objectOf(item.Object)
		spec, err := dock.SpecOf(entry)
		if err != nil {
			continue
		}
		application, installed := applications[keyOf(item.Ref.Namespace, spec.Application)]
		if !installed {
			continue
		}
		view, err := dock.Resolve(entry, application, byNamespace[item.Ref.Namespace])
		if err != nil {
			continue
		}
		rendered := Entry{
			Name:        item.Ref.Name,
			Namespace:   item.Ref.Namespace,
			Application: spec.Application,
			DisplayName: spec.DisplayName,
			Icon:        spec.Icon,
			Visibility:  spec.Visibility,
			Order:       spec.Order,
			Source:      spec.Source,
			IsAvailable: view.IsAvailable,
			Reason:      view.Reason,
		}
		if view.IsAvailable {
			rendered.Endpoint = view.Launch.Endpoint
			rendered.Address = view.Launch.Address
		}
		resolved = append(resolved, rendered)
	}
	sort.SliceStable(resolved, func(left, right int) bool {
		if resolved[left].Order != resolved[right].Order {
			return resolved[left].Order < resolved[right].Order
		}
		return resolved[left].Name < resolved[right].Name
	})
	return resolved
}

// renderApplications lists the installed applications the session may see.
func renderApplications(applications []projection) []Application {
	rendered := make([]Application, 0, len(applications))
	for _, item := range applications {
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		health, _, _ := unstructured.NestedString(item.Object, "status", "health", "state")
		rendered = append(rendered, Application{
			Name:      item.Ref.Name,
			Namespace: item.Ref.Namespace,
			Phase:     phase,
			Health:    health,
		})
	}
	sort.Slice(rendered, func(left, right int) bool {
		if rendered[left].Namespace != rendered[right].Namespace {
			return rendered[left].Namespace < rendered[right].Namespace
		}
		return rendered[left].Name < rendered[right].Name
	})
	return rendered
}

// observe returns the oldest observation behind one item and whether it is
// stale (KOSD-C-13). An item built from several reads is only as fresh as its
// oldest one.
func observe(now time.Time, sources ...[]projection) (*time.Time, bool) {
	var oldest time.Time
	stale := false
	for _, source := range sources {
		for _, item := range source {
			if item.ObservedAt.IsZero() {
				continue
			}
			if oldest.IsZero() || item.ObservedAt.Before(oldest) {
				oldest = item.ObservedAt
			}
			if item.Stale {
				stale = true
			}
		}
	}
	if oldest.IsZero() {
		return nil, false
	}
	observed := oldest.UTC()
	return &observed, stale || now.Sub(oldest) > StaleThreshold
}

// pageObservation is the oldest observation of the server-side items.
func pageObservation(items []Item) (time.Time, bool) {
	var oldest time.Time
	stale := false
	for _, item := range items {
		if item.ObservedAt == nil {
			continue
		}
		if oldest.IsZero() || item.ObservedAt.Before(oldest) {
			oldest = *item.ObservedAt
		}
		stale = stale || item.Stale
	}
	return oldest, stale
}

// renderNotifications marks every event with the account's read state
// (DASH-C-27). The watermark is the moment the account last marked read; an
// event seen after it is unread.
func renderNotifications(read EventRead, watermark time.Time, failure *Failure) NotificationCentre {
	centre := NotificationCentre{
		ObservedAt: read.ObservedAt,
		Window:     read.Window,
		Events:     make([]Notification, 0, len(read.Events)),
	}
	if failure != nil {
		// The events are served by another source; only their read state is
		// missing, so the item states that instead of guessing (KOSD-C-61).
		for _, event := range read.Events {
			centre.Events = append(centre.Events, Notification{Event: event})
		}
		centre.ReadStateUnavailable = true
		centre.ReadStateReason = failure.reason()
		return centre
	}
	unread := 0
	for _, event := range read.Events {
		marked := !watermark.IsZero() && !event.LastSeen.After(watermark)
		if !marked {
			unread++
		}
		centre.Events = append(centre.Events, Notification{Event: event, Read: &marked})
	}
	centre.Unread = &unread
	if !watermark.IsZero() {
		moment := watermark.UTC()
		centre.ReadThrough = &moment
	}
	return centre
}

// actionsOf renders the desktop page's declared actions (DASH-C-22). The
// mark-read write carries the watermark this read observed, so a click covers
// exactly the events the page showed (DASH-C-27); it is left out while the read
// state is unavailable, because the write would be refused too.
func actionsOf(watermark time.Time, readStateAvailable bool) []Action {
	declared := dock.DesktopPage().Actions
	actions := make([]Action, 0, len(declared))
	for _, action := range declared {
		if action.ID == dock.ActionMarkNotificationsRead {
			if !readStateAvailable {
				continue
			}
			actions = append(actions, Action{
				ID:           action.ID,
				RequiredRole: action.RequiredRole,
				Entry: ActionEntry{
					Method: action.Method,
					Path:   action.Path,
					Body:   map[string]any{"value": encodeReadState(watermark)},
				},
			})
			continue
		}
		actions = append(actions, Action{
			ID:           action.ID,
			RequiredRole: action.RequiredRole,
			Entry:        ActionEntry{Method: action.Method, Path: action.Path},
		})
	}
	return actions
}

// actionEntry resolves one rendered action's write.
func actionEntry(actions []Action, id string) (ActionEntry, bool) {
	for _, action := range actions {
		if action.ID == id {
			return action.Entry, true
		}
	}
	return ActionEntry{}, false
}

// objectOf wraps one stored object for the resolvers.
func objectOf(object map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: object}
}

// keyOf names one namespaced object.
func keyOf(namespace, name string) string {
	return namespace + "/" + name
}
