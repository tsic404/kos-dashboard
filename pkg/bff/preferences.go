package bff

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/pkg/dock"
)

// The dock and launcher layout reads (DASH-C-27, DASH-C-30). The value belongs to
// the account, not to the browser: the control plane's preference store is the
// one source, and this front only defaults what the account never wrote and
// orders the applications it lists. Writes go through the proxy unchanged,
// where judgePreferenceWrite refuses a value this product could not read back.
const (
	dockLayoutPath     = "/api/v1/dock"
	launcherLayoutPath = "/api/v1/launcher"
	// preferenceDomain is the extension domain the layouts live in (DASH-C-27).
	preferenceDomain = dock.Identifier
)

// dockView is the dock as the shell reads it: the account's layout plus the
// installed applications in the order the dock shows them. A pin naming an
// application that is not installed keeps its stored place without surfacing an
// entry, so reinstalling it restores the same dock (DASH-C-30).
type dockView struct {
	// Layout is the account's dock layout, defaulted when none is stored.
	dock.DockLayout
	// IsStored reports that the account wrote the value; false means the answer
	// is the default and nothing was written back.
	IsStored bool `json:"isStored"`
	// Applications is the installed application order.
	Applications []string `json:"applications"`
}

// launcherView is the launcher as the shell reads it.
type launcherView struct {
	// Layout is the account's launcher layout, defaulted when none is stored.
	dock.LauncherLayout
	// IsStored reports that the account wrote the value.
	IsStored bool `json:"isStored"`
	// Applications is the application list the launcher shows. The recent
	// order it may sort by is the browser's own source (KOSD-C-53).
	Applications []string `json:"applications"`
}

// dockLayout reads the account's dock layout and its application order.
func (s *Server) dockLayout(w http.ResponseWriter, r *http.Request) {
	token, values, ok := s.desktopPreferences(w, r)
	if !ok {
		return
	}
	registry, err := s.registryFor(r.Context(), token)
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "应用清单读取失败："+err.Error(), "read")
		return
	}
	stored, isStored := values[dock.PreferenceLayout]
	layout := dock.DecodeDockLayout(stored)
	writeJSON(w, http.StatusOK, dockView{
		DockLayout:   layout,
		IsStored:     isStored,
		Applications: dock.PinnedOrder(layout.Pinned, installedIDs(registry)),
	})
}

// launcherLayout reads the account's launcher layout and its application order.
func (s *Server) launcherLayout(w http.ResponseWriter, r *http.Request) {
	token, values, ok := s.desktopPreferences(w, r)
	if !ok {
		return
	}
	registry, err := s.registryFor(r.Context(), token)
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "应用清单读取失败："+err.Error(), "read")
		return
	}
	stored, isStored := values[dock.PreferenceLauncherLayout]
	layout := dock.DecodeLauncherLayout(stored)

	writeJSON(w, http.StatusOK, launcherView{
		LauncherLayout: layout,
		IsStored:       isStored,
		Applications:   installedIDs(registry),
	})
}

// desktopPreferences reads this product's preference domain as the caller's
// session. A refusal is answered here, so the caller reads one envelope.
func (s *Server) desktopPreferences(w http.ResponseWriter, r *http.Request) (string, map[string]string, bool) {
	token, ok := s.sessionToken(r)
	if !ok {
		writeFailure(w, http.StatusUnauthorized, "unauthenticated", "会话无效或已过期，请重新登录", "authorize")
		return "", nil, false
	}
	payload, refused := s.readUpstream(r, token, "/api/v1/preferences/"+preferenceDomain)
	if refused != nil {
		if len(refused.Body) > 0 {
			// The control plane's own envelope travels unchanged (KOSD-C-12).
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(refused.Status)
			_, _ = w.Write(refused.Body)
			return "", nil, false
		}
		writeFailure(w, http.StatusBadGateway, "internal", "偏好读取失败", "read")
		return "", nil, false
	}
	var read struct {
		Items map[string]string `json:"items"`
	}
	if err := json.Unmarshal(payload, &read); err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "偏好响应无法解码："+err.Error(), "read")
		return "", nil, false
	}
	return token, read.Items, true
}

// readUpstream performs one authenticated control-plane read.
func (s *Server) readUpstream(r *http.Request, token, path string) ([]byte, *upstreamFailure) {
	request, err := s.upstreamRequest(r.Context(), http.MethodGet, path, "", nil, token)
	if err != nil {
		return nil, &upstreamFailure{Status: http.StatusBadGateway}
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, &upstreamFailure{Status: http.StatusBadGateway}
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return nil, &upstreamFailure{Status: http.StatusBadGateway}
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, &upstreamFailure{Status: response.StatusCode, Body: payload}
	}
	return payload, nil
}

// upstreamFailure is one refused upstream call: the status it answered and the
// envelope it carried, so the front answers what a direct call answers
// (KOSD-C-12).
type upstreamFailure struct {
	Status int
	Body   []byte
}

// installedIDs names every installed application.
func installedIDs(registry *apphost.Registry) []string {
	apps := registry.Apps()
	ids := make([]string, 0, len(apps))
	for _, entry := range apps {
		ids = append(ids, entry.ID)
	}
	return ids
}
