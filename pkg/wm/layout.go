package wm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The window-layout preference value (DASH-C-27). The account keeps the
// placement of its windows under `dashboard.windowLayout`, so every browser of
// the account lays them out the same way. The value is one versioned collection
// of records; the open set, z-order, focus and minimization are browser session
// state and never appear here (DASH-C-74, KOSD-F-19).
const (
	// WindowLayoutVersion is the schema version this product writes.
	WindowLayoutVersion = 2
	// LegacyWindowLayoutVersion is the session snapshot the window manager
	// wrote before the layout domain existed; a stored value of it migrates.
	LegacyWindowLayoutVersion = 1
	// MaxWindowLayoutRecords bounds one written layout.
	MaxWindowLayoutRecords = 50
	// MaxWindowLayoutBytes bounds one serialized layout value: 64 KiB.
	MaxWindowLayoutBytes = 64 * 1024
	// MaxWindowLayoutCoordinate bounds one record's x and y: far beyond any
	// virtual desktop, so a value past it is corruption rather than a layout.
	MaxWindowLayoutCoordinate = 1 << 20
)

// WindowLayout is the value stored under the window-layout preference key.
type WindowLayout struct {
	// SchemaVersion is the version of this value.
	SchemaVersion int `json:"schemaVersion"`
	// Records is one record per open window, keyed by application and instance.
	Records []Record `json:"records"`
}

// DefaultWindowLayout is the layout an account that stored none gets, and the
// one an unsupported stored value answers with (DASH-C-27).
func DefaultWindowLayout() WindowLayout {
	return WindowLayout{SchemaVersion: WindowLayoutVersion, Records: []Record{}}
}

// Encode renders the layout as the preference value the control plane stores.
func (l WindowLayout) Encode() (string, error) {
	layout := l
	if layout.SchemaVersion == 0 {
		layout.SchemaVersion = WindowLayoutVersion
	}
	if layout.Records == nil {
		layout.Records = []Record{}
	}
	if err := validateLayoutRecords(layout.Records); err != nil {
		return "", err
	}
	return encode(layout)
}

// WindowLayoutRead is one stored layout as this product reads it. An
// unsupported value answers the default layout with IsSupported false and a
// reason, and is left in storage until the account writes again (DASH-C-27).
type WindowLayoutRead struct {
	// Layout is the value this product can use.
	Layout WindowLayout
	// IsSupported reports whether the stored value was honoured; false means
	// Layout is the default.
	IsSupported bool
	// IsMigrated reports that a legacy value was migrated to current.
	IsMigrated bool
	// Reason says why an unsupported value answered the default.
	Reason string
}

// ReadWindowLayout reads one stored layout value. An empty value is the absence
// of a preference: the default layout, supported. A legacy value is migrated to
// the current version in memory; a migration failure and a value of a version
// this product does not support both answer the default (DASH-C-27).
func ReadWindowLayout(value string) WindowLayoutRead {
	if strings.TrimSpace(value) == "" {
		return WindowLayoutRead{Layout: DefaultWindowLayout(), IsSupported: true}
	}
	version, err := probeWindowLayoutVersion(value)
	if err != nil {
		return unsupportedLayout(fmt.Sprintf("布局值无法读取：%v", err))
	}
	switch version {
	case WindowLayoutVersion:
		layout, err := decodeWindowLayout(value)
		if err != nil {
			return unsupportedLayout(fmt.Sprintf("布局值无法读取：%v", err))
		}
		return WindowLayoutRead{Layout: layout, IsSupported: true}
	case LegacyWindowLayoutVersion:
		layout, err := migrateLegacyLayout(value)
		if err != nil {
			return unsupportedLayout(fmt.Sprintf("旧布局无法迁移：%v", err))
		}
		return WindowLayoutRead{Layout: layout, IsSupported: true, IsMigrated: true}
	default:
		if version > WindowLayoutVersion {
			return unsupportedLayout(fmt.Sprintf("布局版本 %d 高于当前支持的版本 %d", version, WindowLayoutVersion))
		}
		return unsupportedLayout(fmt.Sprintf("布局版本 %d 不受当前产品支持（当前版本 %d）", version, WindowLayoutVersion))
	}
}

// unsupportedLayout answers the default layout with the reason it could not be
// read.
func unsupportedLayout(reason string) WindowLayoutRead {
	return WindowLayoutRead{Layout: DefaultWindowLayout(), Reason: reason}
}

// LayoutWriteErrorKind tells why one written layout was refused.
type LayoutWriteErrorKind int

const (
	// LayoutWriteInvalid marks a value this product cannot honour.
	LayoutWriteInvalid LayoutWriteErrorKind = iota
	// LayoutWriteTooMany marks a value above the record limit.
	LayoutWriteTooMany
	// LayoutWriteTooLarge marks a value above the serialized limit.
	LayoutWriteTooLarge
	// LayoutWriteVersionUnsupported marks an incoming version this product
	// cannot judge.
	LayoutWriteVersionUnsupported
	// LayoutWriteStoredNewer marks a write by a client older than the value
	// already stored; the account must upgrade before it writes again.
	LayoutWriteStoredNewer
)

// LayoutWriteError is one refused layout write with the reason a user reads.
type LayoutWriteError struct {
	Kind    LayoutWriteErrorKind
	Message string
}

// Error renders the reason.
func (e *LayoutWriteError) Error() string { return e.Message }

// ParseWindowLayoutWrite judges one layout write against the value already
// stored (DASH-C-27): the incoming value must be one this product can read
// back, it must stay inside the record and serialized limits, and it must not
// overwrite a value a newer version wrote. An unreadable stored value is not a
// barrier, so a write can repair it.
func ParseWindowLayoutWrite(value, stored string) error {
	if strings.TrimSpace(value) == "" {
		return &LayoutWriteError{Kind: LayoutWriteInvalid, Message: "布局值不能为空"}
	}
	if len(value) > MaxWindowLayoutBytes {
		return &LayoutWriteError{
			Kind:    LayoutWriteTooLarge,
			Message: fmt.Sprintf("布局值 %d 字节超过序列化上限 %d 字节", len(value), MaxWindowLayoutBytes),
		}
	}
	incoming, incomingVersion, err := decodeWrittenLayout(value)
	if err != nil {
		return err
	}
	if len(incoming.Records) > MaxWindowLayoutRecords {
		return &LayoutWriteError{
			Kind:    LayoutWriteTooMany,
			Message: fmt.Sprintf("布局记录 %d 条超过上限 %d 条", len(incoming.Records), MaxWindowLayoutRecords),
		}
	}
	if err := validateLayoutCoordinates(incoming.Records); err != nil {
		return err
	}
	storedVersion, err := storedWindowLayoutVersion(stored)
	if err != nil {
		// The stored value cannot be read, so it holds no version this write
		// could overwrite; the write repairs it.
		return nil
	}
	if storedVersion > WindowLayoutVersion {
		return &LayoutWriteError{
			Kind: LayoutWriteStoredNewer,
			Message: fmt.Sprintf("存储的布局版本 %d 高于当前支持的版本 %d，请升级桌面后再修改",
				storedVersion, WindowLayoutVersion),
		}
	}
	if storedVersion > incomingVersion {
		return &LayoutWriteError{
			Kind: LayoutWriteStoredNewer,
			Message: fmt.Sprintf("存储的布局版本 %d 高于本次写入的版本 %d，请升级桌面后再修改",
				storedVersion, incomingVersion),
		}
	}
	return nil
}

// AdaptWindowLayout returns the layout as one browser sees it (DASH-C-27): a
// record whose display the browser no longer has falls back to the primary
// display (the empty identifier), and bounds are clamped into the viewport. The
// answer is a copy; a read never writes the adaptation back.
//
// displays is the set of non-primary displays the browser reports. nil means
// the caller did not say, so no display falls back; a non-nil set always
// contains the primary display.
func AdaptWindowLayout(layout WindowLayout, viewport *Size, displays []string) WindowLayout {
	records := make([]Record, len(layout.Records))
	copy(records, layout.Records)
	adapted := WindowLayout{SchemaVersion: layout.SchemaVersion, Records: records}
	hasDisplays := displays != nil
	present := make(map[string]bool, len(displays)+1)
	present[""] = true
	for _, display := range displays {
		present[display] = true
	}
	for index := range adapted.Records {
		record := &adapted.Records[index]
		if hasDisplays && record.Display != "" && !present[record.Display] {
			record.Display = ""
		}
		if viewport != nil && viewport.Width > 0 && viewport.Height > 0 {
			clampRecordBounds(record, *viewport)
		}
	}
	return adapted
}

// clampRecordBounds keeps one normal rectangle inside the viewport, shrinking
// an oversized window and moving an off-screen one into view.
func clampRecordBounds(record *Record, viewport Size) {
	if record.Width > viewport.Width {
		record.Width = viewport.Width
	}
	if record.Height > viewport.Height {
		record.Height = viewport.Height
	}
	if record.X < 0 {
		record.X = 0
	}
	if record.Y < 0 {
		record.Y = 0
	}
	// Compare before subtracting: a coordinate near the integer limit would
	// overflow in X+Width and pass the comparison, leaving the record off
	// screen. Width is at most the viewport's by now, so the sum below cannot
	// overflow and the right edge stays non-negative.
	if record.X > viewport.Width-record.Width {
		record.X = viewport.Width - record.Width
	}
	if record.Y > viewport.Height-record.Height {
		record.Y = viewport.Height - record.Height
	}
}

// probeWindowLayoutVersion reads the version one stored value declares; a value
// without schemaVersion is the legacy session snapshot (DASH-C-27).
func probeWindowLayoutVersion(value string) (int, error) {
	var probe struct {
		SchemaVersion *int            `json:"schemaVersion"`
		Windows       json.RawMessage `json:"windows"`
	}
	if err := json.Unmarshal([]byte(value), &probe); err != nil {
		return 0, err
	}
	if probe.SchemaVersion != nil {
		return *probe.SchemaVersion, nil
	}
	if len(probe.Windows) > 0 {
		return LegacyWindowLayoutVersion, nil
	}
	return 0, fmt.Errorf("布局值既没有 schemaVersion 也没有布局记录")
}

// storedWindowLayoutVersion reads the version a stored value carries; an
// unreadable value reports no usable version.
func storedWindowLayoutVersion(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	return probeWindowLayoutVersion(value)
}

// decodeWrittenLayout decodes one written value and reports the version it
// declared. A legacy value is migrated so its records are judged in the current
// shape.
func decodeWrittenLayout(value string) (WindowLayout, int, error) {
	version, err := probeWindowLayoutVersion(value)
	if err != nil {
		return WindowLayout{}, 0, &LayoutWriteError{
			Kind: LayoutWriteInvalid, Message: "布局请求体不是合法 JSON：" + err.Error(),
		}
	}
	switch version {
	case WindowLayoutVersion:
		layout, err := decodeWindowLayout(value)
		if err != nil {
			return WindowLayout{}, 0, &LayoutWriteError{Kind: LayoutWriteInvalid, Message: err.Error()}
		}
		return layout, version, nil
	case LegacyWindowLayoutVersion:
		layout, err := migrateLegacyLayout(value)
		if err != nil {
			return WindowLayout{}, 0, &LayoutWriteError{
				Kind: LayoutWriteInvalid, Message: "旧布局无法迁移：" + err.Error(),
			}
		}
		return layout, version, nil
	default:
		return WindowLayout{}, 0, &LayoutWriteError{
			Kind: LayoutWriteVersionUnsupported,
			Message: fmt.Sprintf("布局版本 %d 不受当前产品支持（当前版本 %d）",
				version, WindowLayoutVersion),
		}
	}
}

// decodeWindowLayout reads one current-version value strictly.
func decodeWindowLayout(value string) (WindowLayout, error) {
	var layout WindowLayout
	decoder := json.NewDecoder(bytes.NewReader([]byte(value)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&layout); err != nil {
		return WindowLayout{}, fmt.Errorf("布局值字段无效：%w", err)
	}
	if layout.SchemaVersion != WindowLayoutVersion {
		return WindowLayout{}, fmt.Errorf("布局版本 %d 与当前版本 %d 不一致", layout.SchemaVersion, WindowLayoutVersion)
	}
	if layout.Records == nil {
		layout.Records = []Record{}
	}
	if err := validateLayoutRecords(layout.Records); err != nil {
		return WindowLayout{}, err
	}
	return layout, nil
}

// migrateLegacyLayout reads one legacy session snapshot and maps its placements
// to records of the current version. Minimization is session-only, so it is
// dropped here and never enters the layout domain (DASH-C-74).
func migrateLegacyLayout(value string) (WindowLayout, error) {
	var legacy struct {
		SchemaVersion *int        `json:"schemaVersion"`
		Windows       []Placement `json:"windows"`
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(value)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return WindowLayout{}, fmt.Errorf("旧布局字段无效：%w", err)
	}
	records := make([]Record, 0, len(legacy.Windows))
	for _, placement := range legacy.Windows {
		records = append(records, Record{
			Application: placement.Application,
			Instance:    placement.Instance,
			X:           placement.X,
			Y:           placement.Y,
			Width:       placement.Width,
			Height:      placement.Height,
			State:       StateNormal,
			Workspace:   WorkspaceDefault,
		})
	}
	if err := validateLayoutRecords(records); err != nil {
		return WindowLayout{}, err
	}
	return WindowLayout{SchemaVersion: WindowLayoutVersion, Records: records}, nil
}

// validateLayoutCoordinates refuses a record whose x or y is far outside any
// real desktop. A stored value keeps such a coordinate out of the clamp
// arithmetic, which compares coordinates it did not produce.
func validateLayoutCoordinates(records []Record) error {
	for index, record := range records {
		if record.X < -MaxWindowLayoutCoordinate || record.X > MaxWindowLayoutCoordinate {
			return &LayoutWriteError{
				Kind: LayoutWriteInvalid,
				Message: fmt.Sprintf("第 %d 条布局记录的横坐标 %d 超出 ±%d 的允许范围",
					index+1, record.X, MaxWindowLayoutCoordinate),
			}
		}
		if record.Y < -MaxWindowLayoutCoordinate || record.Y > MaxWindowLayoutCoordinate {
			return &LayoutWriteError{
				Kind: LayoutWriteInvalid,
				Message: fmt.Sprintf("第 %d 条布局记录的纵坐标 %d 超出 ±%d 的允许范围",
					index+1, record.Y, MaxWindowLayoutCoordinate),
			}
		}
	}
	return nil
}

// validateLayoutRecords judges every record of one layout. A record names one
// window by application and instance key and carries only layout fields: a
// session state such as minimization or a session field such as z-order fails
// here, so it can never enter the preference domain (DASH-C-74, KOSD-F-19).
func validateLayoutRecords(records []Record) error {
	seen := make(map[string]bool, len(records))
	for index, record := range records {
		position := index + 1
		if strings.TrimSpace(record.Application) == "" {
			return fmt.Errorf("第 %d 条布局记录缺少应用标识", position)
		}
		if record.Instance < 1 {
			return fmt.Errorf("第 %d 条布局记录的实例键 %d 无效", position, record.Instance)
		}
		if record.Width <= 0 || record.Height <= 0 {
			return fmt.Errorf("第 %d 条布局记录的边界 %dx%d 无效", position, record.Width, record.Height)
		}
		switch record.State {
		case StateNormal, StateMaximized, StateFullscreen, StateSnapped:
		default:
			return fmt.Errorf("第 %d 条布局记录的状态 %q 不属于布局域，最小化与打开集合留在浏览器会话",
				position, record.State)
		}
		if strings.TrimSpace(record.Workspace) == "" {
			return fmt.Errorf("第 %d 条布局记录缺少工作区", position)
		}
		key := fmt.Sprintf("%s#%d", record.Application, record.Instance)
		if seen[key] {
			return fmt.Errorf("第 %d 条布局记录重复了 %s 的第 %d 个实例", position, record.Application, record.Instance)
		}
		seen[key] = true
	}
	return nil
}
