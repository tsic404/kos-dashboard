package bff

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tsic404/kos-dashboard/pkg/dock"
	"github.com/tsic404/kos-dashboard/pkg/wm"
)

// The window-layout preference read (DASH-C-27). The value belongs to the
// account and lives in the control plane's preference store; this front reads
// it through the caller's session and answers it adapted to the browser that
// asked: bounds clamped into the reported viewport, and a record whose display
// the browser no longer reports falling back to the primary display. The
// adaptation is the answer's alone — a read writes nothing back, so the stored
// value keeps the rectangle the user last chose until they change the layout
// again.
//
// Writes go through the proxy unchanged; judgePreferenceWrite refuses a value
// this product could not read back before the control plane stores it.
const desktopLayoutPath = "/api/v1/desktop/layout"

// layoutView is the window layout as one browser reads it.
type layoutView struct {
	// SchemaVersion is the version of the layout this answer carries.
	SchemaVersion int `json:"schemaVersion"`
	// Records is one record per window, adapted to the asking browser.
	Records []wm.Record `json:"records"`
	// IsStored reports that the account wrote a layout; false means the answer
	// is the default and nothing was written back.
	IsStored bool `json:"isStored"`
	// IsSupported reports that the stored value was honoured. False means an
	// unsupported value answered the default and stays stored untouched.
	IsSupported bool `json:"isSupported"`
	// Reason says why an unsupported value answered the default.
	Reason string `json:"reason,omitempty"`
}

// desktopLayout reads the account's window layout as this browser sees it. The
// viewport and the displays are the browser's own report, so the same stored
// layout answers two browsers differently without either read writing it back
// (DASH-C-27).
func (s *Server) desktopLayout(w http.ResponseWriter, r *http.Request) {
	_, values, ok := s.preferencesFor(w, r, preferenceDomain)
	if !ok {
		return
	}
	viewport, err := parseViewport(r.URL.Query().Get("viewport"))
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", err.Error(), "validate")
		return
	}
	stored, isStored := values[dock.PreferenceWindowLayout]
	read := wm.ReadWindowLayout(stored)
	adapted := wm.AdaptWindowLayout(read.Layout, viewport, reportedDisplays(r))
	answer := layoutView{
		SchemaVersion: adapted.SchemaVersion,
		Records:       adapted.Records,
		IsStored:      isStored,
		IsSupported:   read.IsSupported,
		Reason:        read.Reason,
	}
	writeJSON(w, http.StatusOK, answer)
}

// reportedDisplays reads the displays one browser reports. A request without
// the parameter says nothing about them, so no record falls back; a request
// that names its displays declares a complete set, so a record naming another
// one falls back to the primary display.
func reportedDisplays(r *http.Request) []string {
	values, reported := r.URL.Query()["display"]
	if !reported {
		return nil
	}
	displays := make([]string, 0, len(values))
	for _, value := range values {
		if name := strings.TrimSpace(value); name != "" {
			displays = append(displays, name)
		}
	}
	return displays
}

// parseViewport reads one browser viewport, "1440x900". No value means the
// browser did not report one, so nothing is clamped.
func parseViewport(value string) (*wm.Size, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(name, func(letter rune) bool {
		return letter == 'x' || letter == 'X' || letter == '×' || letter == ','
	})
	if len(parts) != 2 {
		return nil, fmt.Errorf("视口 %q 不是 宽x高", name)
	}
	width, errWidth := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, errHeight := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errWidth != nil || errHeight != nil || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("视口 %q 不是 宽x高：宽与高须为正整数", name)
	}
	return &wm.Size{Width: width, Height: height}, nil
}

// judgeWindowLayoutWrite judges one write to the window-layout key against the
// value the account already holds (DASH-C-27). A stored value the front cannot
// read is not a barrier, so a write can repair it. A control-plane read that
// fails answers as the read does: the preference is not writable while the
// control-plane database is unavailable (KOSD-C-61), and the control plane's own
// envelope travels unchanged (KOSD-C-12).
func (s *Server) judgeWindowLayoutWrite(w http.ResponseWriter, r *http.Request, token, domain, value string) bool {
	payload, refused := s.readUpstream(r, token, "/api/v1/preferences/"+domain)
	if refused != nil {
		if len(refused.Body) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(refused.Status)
			_, _ = w.Write(refused.Body)
			return false
		}
		writeFailure(w, http.StatusBadGateway, "internal", "存储的布局读取失败", "read")
		return false
	}
	var stored struct {
		Items map[string]string `json:"items"`
	}
	if err := json.Unmarshal(payload, &stored); err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "存储的布局响应无法解码："+err.Error(), "read")
		return false
	}
	refusal := wm.ParseWindowLayoutWrite(value, stored.Items[dock.PreferenceWindowLayout])
	if refusal == nil {
		return true
	}
	status := http.StatusBadRequest
	code := "invalid_request"
	var layoutRefusal *wm.LayoutWriteError
	if errors.As(refusal, &layoutRefusal) && layoutRefusal.Kind == wm.LayoutWriteStoredNewer {
		status = http.StatusConflict
		code = "failed_precondition"
	}
	writeFailure(w, status, code, refusal.Error(), "validate")
	return false
}
