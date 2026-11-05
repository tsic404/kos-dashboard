// Package dock contributes the desktop surface of kos-dashboard: the desktop
// entry object class (C-30), the desktop page (C-52) and the preference
// domains (C-27) for the dock layout and the notification read state. Entries
// are user-written; their lifecycle is the declared controller's (C-20), whose
// host contract is on Controller.
//
// The contribution contract (C-18, C-22) and the preference store (C-27) stay
// in kosd; this package only declares what the product serves.
package dock

import (
	"net/http"

	"github.com/tsic404/kosd/api"
	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/pkg/write"
)

// The package identity and the contribution names it declares.
const (
	// Identifier is the extension identity across versions (C-17).
	Identifier = "kos-dashboard"
	// Version is the package version this declaration belongs to.
	Version = "1.0.0"
	// APIGroup is the extension's own API group; it is never the kernel group
	// (C-19).
	APIGroup = "desktop.kos.tsic.top"
	// EntryKind is the desktop entry object class (C-30).
	EntryKind = "DesktopEntry"
	// EntryResource is the served resource name of the class.
	EntryResource = "desktopentries"
	// EntryVersion is the served version of the class.
	EntryVersion = "v1"
	// PageID is the desktop page identity (C-52).
	PageID = "desktop"
	// PreferenceDock is the dock preference domain: the layout key (C-27).
	PreferenceDock = "dock"
	// PreferenceLayout is the dock layout preference key (C-27).
	PreferenceLayout = "layout"
	// PreferenceNotifications is the notification preference domain: the
	// account's read state (C-27, C-52).
	PreferenceNotifications = "notifications"
	// PreferenceRead is the notification read key in that domain.
	PreferenceRead = "read"
)

// Visibilities an entry may carry (C-30). The empty value reads as Everyone.
const (
	// VisibilityEveryone shows the entry to every signed-in account.
	VisibilityEveryone = "everyone"
	// VisibilityOwner shows the entry to the account that owns it.
	VisibilityOwner = "owner"
)

// The page actions the desktop page offers: control-plane writes on the entry
// class and on the account's preference domain (C-22, C-27). Opening an entry
// is not among them, because a window is browser state and writes nothing. The
// shell fills {name} with the entry it acts on and {namespace} with that
// entry's namespace; a create names its object in the body, so its path carries
// no {name}.
const (
	// ActionAddEntry creates one desktop entry.
	ActionAddEntry = "add-entry"
	// ActionMoveEntry rewrites the visibility and order of one entry.
	ActionMoveEntry = "move-entry"
	// ActionRemoveEntry deletes one entry without touching its application.
	ActionRemoveEntry = "remove-entry"
	// ActionMarkNotificationsRead stores the account's notification read
	// watermark, so the read state follows the account across browsers
	// (C-27, C-52).
	ActionMarkNotificationsRead = "mark-notifications-read"
)

// entryCollectionPath addresses the entry class for a create. The apiVersion
// names the extension group and the namespace query is the entry's namespace.
func entryCollectionPath() string {
	return "/api/v1/resources/" + EntryKind + "?apiVersion=" + APIGroup + "/" + EntryVersion +
		"&namespace={namespace}"
}

// entryObjectPath addresses one existing entry: {name} and {namespace}
// identify it and the apiVersion names the extension group.
func entryObjectPath() string {
	return "/api/v1/resources/" + EntryKind + "/{name}?apiVersion=" + APIGroup + "/" + EntryVersion +
		"&namespace={namespace}"
}

// notificationReadPath addresses the signed-in account's notification read
// preference (C-27). The value is the write body's business, so the path names
// only the domain and the key.
func notificationReadPath() string {
	return "/api/v1/preferences/" + Identifier + "/" + PreferenceRead
}

// Declaration is the kos-dashboard package (C-18): the entry class, the desktop
// page and the preference domains the page writes.
func Declaration() extension.Declaration {
	return extension.Declaration{
		Identifier: Identifier,
		Version:    Version,
		APIGroup:   APIGroup,
		Contributions: extension.Contributions{
			ObjectClasses: []extension.ObjectClass{{
				Group: APIGroup, Kind: EntryKind, Resource: EntryResource,
				Versions: []string{EntryVersion}, Namespaced: true,
				// The entry names its application as a declarative reference
				// (C-19), so the kernel blocks deleting that application while
				// the entry exists and lists the referencing entries.
				References: []string{ApplicationRefPath},
			}},
			Controllers: []extension.Controller{{
				// The lifecycle controller writes this package's class alone
				// (C-20): it removes the entries of an application that is
				// going away and keeps an installed application's entries.
				ID:      EntryControllerID,
				Manages: []extension.GroupKind{{Group: APIGroup, Kind: EntryKind}},
				Scope:   extension.WriteScope{Groups: []string{APIGroup}},
			}},
			Pages: []extension.Page{DesktopPage()},
			Preferences: []extension.PreferenceSchema{
				{ID: PreferenceDock, Keys: []string{PreferenceLayout}},
				{ID: PreferenceNotifications, Keys: []string{PreferenceRead}},
			},
		},
		Permissions: []extension.Permission{
			{
				// The desktop page is visible to every signed-in account; a
				// Guest reads it and cannot write entries (C-59).
				Contribution: PageID,
				RequiredRole: string(write.RoleGuest),
			},
			{
				// C-59's default for an extension object class: the global
				// Owner writes entries and every other role reads them.
				Contribution:  EntryKind,
				ObjectWriters: []write.Role{write.RoleOwner},
			},
			{
				// The controller reads the kernel application each entry
				// names; its write scope stays this package's own group.
				Contribution: EntryControllerID,
				ReadScope: extension.ReadScope{
					Kinds: []extension.GroupKind{{Group: api.Group, Kind: ApplicationKind}},
				},
			},
		},
		Retention: []extension.Retention{
			// Entries are user-authored desktop state; an uninstall keeps them
			// so a reinstall restores the same desktop (C-29).
			{Resource: EntryResource, Keep: true},
			// Dock preferences are per-account UI state and follow C-27's
			// default: an uninstall deletes them.
			{Resource: extension.ClassPreferences, Keep: false},
		},
	}
}

// DesktopPage is the desktop C-52 describes: the entries, the installed
// applications, the recent list the browser holds and the notification centre's
// recent events. Entries carry the modification entry; the notification centre
// carries the account's read-state write.
func DesktopPage() extension.Page {
	return extension.Page{
		ID:           PageID,
		Title:        "桌面",
		Location:     "desktop",
		RequiredRole: string(write.RoleGuest),
		Items: []extension.PageItem{
			{
				ID: "entries", Label: "桌面条目",
				Source: extension.PageSourceObject, Reference: EntryKind,
				Action: ActionRemoveEntry,
			},
			{
				ID: "applications", Label: "已安装应用",
				Source: extension.PageSourceObject, Reference: "Application",
				Reason: "桌面只打开应用，不修改应用",
			},
			{
				ID: "recent", Label: "最近使用",
				Source: extension.PageSourceLocal, Store: "desktop.recent",
				Reason: "最近使用只存在于浏览器",
			},
			{
				ID: "notifications", Label: "通知中心",
				Source: extension.PageSourceObject, Reference: "Event",
				Action: ActionMarkNotificationsRead,
			},
		},
		Actions: []extension.Action{
			{ID: ActionAddEntry, Method: http.MethodPost, Path: entryCollectionPath(),
				RequiredRole: string(write.RoleOwner)},
			{ID: ActionMoveEntry, Method: http.MethodPut, Path: entryObjectPath(),
				RequiredRole: string(write.RoleOwner)},
			{ID: ActionRemoveEntry, Method: http.MethodDelete, Path: entryObjectPath(),
				RequiredRole: string(write.RoleOwner)},
			{ID: ActionMarkNotificationsRead, Method: http.MethodPut, Path: notificationReadPath(),
				RequiredRole: string(write.RoleMember)},
		},
	}
}
