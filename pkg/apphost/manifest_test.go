package apphost_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// validManifest is one manifest that satisfies every required field.
const validManifest = `{
	"id": "files",
	"name": "文件",
	"version": "1.0.0",
	"icon": "https://files.apps.example.test/assets/icon.svg",
	"entry": "https://files.apps.example.test/index.html",
	"window": {"width": 1024, "height": 720, "minWidth": 480, "minHeight": 360,
		"resizable": true, "mode": "single", "defaultState": "normal"},
	"permissions": ["notifications"],
	"deepLinks": ["/files", "/folders"]
}`

// fixture decodes the valid manifest into a mutable document.
func manifestDoc(t *testing.T) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(validManifest), &document); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return document
}

// encode renders one mutable document.
func encode(t *testing.T, document map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return raw
}

// windowOf reads the window object of one fixture.
func windowOf(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	window, ok := document["window"].(map[string]any)
	if !ok {
		t.Fatalf("fixture carries no window: %v", document)
	}
	return window
}

func TestDecodeManifestReadsTheDeclaredFields(t *testing.T) {
	manifest, err := apphost.DecodeManifest([]byte(validManifest))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := manifest.Validate(apphost.ValidationOptions{ConsoleOrigin: "https://console.example.test"}); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if manifest.ID != "files" || manifest.Window.Mode != apphost.WindowModeSingle {
		t.Fatalf("manifest = %+v, want the declared identity and mode", manifest)
	}
	if got := manifest.Origin(); got != "https://files.apps.example.test" {
		t.Fatalf("origin = %q, want the entry's origin", got)
	}
	if !manifest.ResolveDeepLink("/folders/42") || manifest.ResolveDeepLink("/other") {
		t.Fatalf("deep link resolution does not follow the declared prefixes")
	}
}

func TestDecodeManifestRefusesMissingRequiredFields(t *testing.T) {
	for _, field := range []string{"entry", "window", "icon", "id", "permissions"} {
		document := manifestDoc(t)
		if field == "permissions" {
			delete(document, field)
		} else {
			delete(document, field)
		}
		if _, err := apphost.DecodeManifest(encode(t, document)); err == nil {
			t.Fatalf("manifest without %q decoded, want a refusal (KOSD-C-73)", field)
		}
	}
}

func TestDecodeManifestRefusesUnknownFields(t *testing.T) {
	document := manifestDoc(t)
	document["unknownField"] = true
	if _, err := apphost.DecodeManifest(encode(t, document)); err == nil {
		t.Fatal("manifest with an unknown field decoded, want a refusal")
	}
}

func TestDecodeManifestRefusesPartialWindow(t *testing.T) {
	for _, field := range []string{"width", "height", "minWidth", "minHeight", "resizable", "mode", "defaultState"} {
		document := manifestDoc(t)
		delete(windowOf(t, document), field)
		if _, err := apphost.DecodeManifest(encode(t, document)); err == nil {
			t.Fatalf("window without %q decoded, want a refusal", field)
		}
	}
}

func TestValidateRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]any)
		option apphost.ValidationOptions
		want   string
	}{
		"plain http entry": {
			mutate: func(document map[string]any) { document["entry"] = "http://files.apps.example.test/index.html" },
			want:   "HTTPS",
		},
		"console origin": {
			mutate: func(document map[string]any) { document["entry"] = "https://console.example.test/files" },
			option: apphost.ValidationOptions{ConsoleOrigin: "https://console.example.test"},
			want:   "console origin",
		},
		"icon on another origin": {
			mutate: func(document map[string]any) { document["icon"] = "https://cdn.example.test/icon.svg" },
			want:   "application origin",
		},
		"unknown permission": {
			mutate: func(document map[string]any) { document["permissions"] = []any{"camera"} },
			want:   "unknown permission",
		},
		"minimum above default": {
			mutate: func(document map[string]any) { windowOf(t, document)["minWidth"] = 2048 },
			want:   "minimum size",
		},
		"unknown mode": {
			mutate: func(document map[string]any) { windowOf(t, document)["mode"] = "many" },
			want:   "window mode",
		},
		"unknown default state": {
			mutate: func(document map[string]any) { windowOf(t, document)["defaultState"] = "snapped" },
			want:   "default state",
		},
		"absolute deep link": {
			mutate: func(document map[string]any) { document["deepLinks"] = []any{"https://elsewhere.test/x"} },
			want:   "not a path",
		},
		"backend without health": {
			mutate: func(document map[string]any) { document["backend"] = map[string]any{"type": "http"} },
			want:   "health",
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			document := manifestDoc(t)
			test.mutate(document)
			manifest, err := apphost.DecodeManifest(encode(t, document))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			err = manifest.Validate(test.option)
			if err == nil {
				t.Fatalf("validate accepted %+v, want %q", manifest, test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate error = %q, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestValidateAcceptsLoopbackHTTPWhenAllowed(t *testing.T) {
	manifest := apphost.Manifest{
		ID: "local", Name: "本地", Version: "1.0.0",
		Icon:  "icon.svg",
		Entry: "http://127.0.0.1:9000/index.html",
		Window: apphost.WindowDefaults{
			Width: 800, Height: 600, MinWidth: 320, MinHeight: 240,
			Resizable: true, Mode: apphost.WindowModeMulti, DefaultState: apphost.DefaultStateNormal,
		},
	}
	if err := manifest.Validate(apphost.ValidationOptions{AllowInsecureEntries: true}); err != nil {
		t.Fatalf("loopback entry refused although the deployment allows it: %v", err)
	}
	if err := manifest.Validate(apphost.ValidationOptions{}); err == nil {
		t.Fatal("loopback entry accepted without allowing insecure entries")
	}
}
