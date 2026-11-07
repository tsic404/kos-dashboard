package bff

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tsic404/kos-dashboard/pkg/dock"
)

// TestPreferenceWritesAreJudgedBeforeStoring checks that the front refuses a
// dock or launcher value the desktop could not read back, so a value the
// product cannot honour never reaches the account's store (DASH-C-27).
func TestPreferenceWritesAreJudgedBeforeStoring(t *testing.T) {
	_, upstream := newFakeControlPlane(t, map[string]http.HandlerFunc{
		"POST /api/v1/auth/login": loginHandlers()["POST /api/v1/auth/login"],
		"PUT /api/v1/preferences/" + dock.Identifier + "/" + dock.PreferenceLayout: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
		"PUT /api/v1/preferences/" + dock.Identifier + "/" + dock.PreferenceLauncherLayout: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})
	front := newFront(t, upstream)
	response, _ := do(t, front, http.MethodPost, "/api/v1/auth/login", "", strings.NewReader(`{"username":"alice","password":"secret"}`))
	cookies := sessionCookie + "=" + cookieValue(response, sessionCookie) + "; " + csrfCookie + "=" + cookieValue(response, csrfCookie)
	csrf := cookieValue(response, csrfCookie)

	put := func(path, value string) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodPut, front.URL+path, strings.NewReader(`{"value":`+value+`}`))
		if err != nil {
			t.Fatalf("build the write: %v", err)
		}
		request.Header.Set("Cookie", cookies)
		request.Header.Set(csrfHeader, csrf)
		request.Header.Set("Content-Type", "application/json")
		answer, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		defer answer.Body.Close()
		return answer.StatusCode
	}

	dockPath := "/api/v1/preferences/" + dock.Identifier + "/" + dock.PreferenceLayout
	if status := put(dockPath, `"{\"schemaVersion\":1,\"position\":\"top\",\"autoHide\":false,\"pinned\":[]}"`); status != http.StatusBadRequest {
		t.Fatalf("invalid dock layout = %d, want %d", status, http.StatusBadRequest)
	}
	if status := put(dockPath, `"{\"schemaVersion\":1,\"position\":\"left\",\"autoHide\":true,\"pinned\":[\"files\"]}"`); status != http.StatusNoContent {
		t.Fatalf("valid dock layout = %d, want %d", status, http.StatusNoContent)
	}

	launcherPath := "/api/v1/preferences/" + dock.Identifier + "/" + dock.PreferenceLauncherLayout
	if status := put(launcherPath, `"{\"schemaVersion\":1,\"sort\":\"chaos\",\"categories\":[]}"`); status != http.StatusBadRequest {
		t.Fatalf("invalid launcher layout = %d, want %d", status, http.StatusBadRequest)
	}
}

// appContribution renders one installed application for the registry read.
func appContribution(id, name string) map[string]any {
	return map[string]any{
		"extension": id + "-ext", "component": id, "phase": "Ready", "digest": "sha256:abc",
		"manifest": map[string]any{
			"id": id, "name": name, "version": "1.0.0",
			"icon":  "https://" + id + ".apps.example.test/icon.svg",
			"entry": "https://" + id + ".apps.example.test/index.html",
			"window": map[string]any{
				"width": 800, "height": 600, "minWidth": 320, "minHeight": 240,
				"resizable": true, "mode": "single", "defaultState": "normal",
			},
			"permissions": []string{},
		},
	}
}

// TestDockAndLauncherReadTheAccountLayout checks that both surfaces answer the
// stored layout, default it when the account wrote none, and order the installed
// applications by the pins (DASH-C-27, DASH-C-30).
func TestDockAndLauncherReadTheAccountLayout(t *testing.T) {
	_, upstream := newFakeControlPlane(t, map[string]http.HandlerFunc{
		"POST /api/v1/auth/login": loginHandlers()["POST /api/v1/auth/login"],
		"GET /api/v1/preferences/" + dock.Identifier: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"extension":"kos-dashboard","items":{"layout":"{\"schemaVersion\":1,\"position\":\"left\",\"autoHide\":true,\"pinned\":[\"notes\"]}"}}`))
		},
		"GET /api/v1/apps": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []any{appContribution("files", "文件"), appContribution("notes", "笔记")},
			})
		},
	})
	front := newFront(t, upstream)
	response, _ := do(t, front, http.MethodPost, "/api/v1/auth/login", "", strings.NewReader(`{"username":"alice","password":"secret"}`))
	cookies := sessionCookie + "=" + cookieValue(response, sessionCookie)

	dockRead, body := do(t, front, http.MethodGet, "/api/v1/dock", cookies, nil)
	if dockRead.StatusCode != http.StatusOK {
		t.Fatalf("read the dock = %d %v", dockRead.StatusCode, body)
	}
	if body["position"] != "left" || body["autoHide"] != true || body["isStored"] != true {
		t.Fatalf("dock = %v, want the stored layout", body)
	}
	applications, _ := body["applications"].([]any)
	if len(applications) != 2 || applications[0] != "notes" || applications[1] != "files" {
		t.Fatalf("dock applications = %v, want the pinned application first", applications)
	}

	launcherRead, body := do(t, front, http.MethodGet, "/api/v1/launcher", cookies, nil)
	if launcherRead.StatusCode != http.StatusOK {
		t.Fatalf("read the launcher = %d %v", launcherRead.StatusCode, body)
	}
	if body["sort"] != dock.LauncherSortName || body["isStored"] != false {
		t.Fatalf("launcher = %v, want the default layout without a stored value", body)
	}
}
