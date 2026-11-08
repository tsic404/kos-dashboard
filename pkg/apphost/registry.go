package apphost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The component phases an installed application contribution may carry
// (KOSD-C-28). Only a component whose contributions are loaded serves its
// `App`; a disabled, terminating, unavailable or incompatible component serves
// none, so its entry disappears from the launcher, the dock and the deep-link
// resolver alike.
const (
	// PhaseReady is a component whose declared contributions are active.
	PhaseReady = "Ready"
	// PhaseDegraded is a component with one or more failed contribution
	// classes; the classes that loaded stay active.
	PhaseDegraded = "Degraded"
	// PhaseDisabled is a component whose contributions were withdrawn.
	PhaseDisabled = "Disabled"
	// PhaseTerminating is a component whose deletion is under way.
	PhaseTerminating = "Terminating"
	// PhaseUnavailable is a component that could not install or whose
	// dependency is unmet.
	PhaseUnavailable = "Unavailable"
	// PhaseIncompatible is a component whose target framework this kernel does
	// not serve.
	PhaseIncompatible = "Incompatible"
)

// The surfaces every loaded application appears on (DASH-C-30).
const (
	// SurfaceLauncher is the application list and its search.
	SurfaceLauncher = "launcher"
	// SurfaceDock is the pinned-application bar.
	SurfaceDock = "dock"
)

// Contribution is one installed extension's `App` contribution as the control
// plane reports it: the component it belongs to, that component's phase and the
// manifest the extension package declared.
type Contribution struct {
	// Extension is the extension identity the contribution belongs to.
	Extension string `json:"extension"`
	// Component is the system component the extension was installed through.
	Component string `json:"component"`
	// Phase is the component phase (KOSD-C-28).
	Phase string `json:"phase"`
	// Digest is the installed package digest in sha256:<hex> form; UI assets
	// are addressed by it (KOSD-C-73). Empty means the control plane reported
	// none, which leaves the assets unverifiable.
	Digest string `json:"digest,omitempty"`
	// Manifest is the `App` declaration, verbatim (KOSD-C-73).
	Manifest json.RawMessage `json:"manifest"`
}

// Source reads the installed `App` contributions. The control plane owns the
// package, its digest and the component lifecycle; this product only judges
// what it reads, so a source that cannot answer is an error rather than an
// empty list.
type Source interface {
	// Contributions returns one item per installed `App` contribution.
	Contributions(ctx context.Context) ([]Contribution, error)
}

// Entry is one loaded application: its manifest, the extension that declared
// it and the surfaces it appears on.
type Entry struct {
	Manifest
	// Extension is the extension identity that declared the application.
	Extension string `json:"extension"`
	// Component is the system component the extension is installed as.
	Component string `json:"component"`
	// Digest is the installed package digest the UI assets are addressed by
	// (KOSD-C-73).
	Digest string `json:"digest,omitempty"`
	// Surfaces are the desktop places the application appears on (DASH-C-30).
	Surfaces []string `json:"surfaces"`
}

// Refusal is a contribution that produced no entry, with the reason. It keeps
// the failure observable without exposing the application anywhere.
type Refusal struct {
	// Extension is the extension identity the contribution belongs to.
	Extension string `json:"extension"`
	// Component is the system component name.
	Component string `json:"component"`
	// App is the identity the manifest named, when it got that far.
	App string `json:"app,omitempty"`
	// Reason is why the contribution produced no entry.
	Reason string `json:"reason"`
}

// Registry is the set of applications the launcher, the dock and the deep-link
// resolver serve. A contribution is loaded only when its component is active
// and its manifest satisfies KOSD-C-73; anything else is a refusal, so an
// incomplete declaration never surfaces a half-usable entry.
type Registry struct {
	entries  []Entry
	byID     map[string]int
	refusals []Refusal
}

// Load judges every contribution and builds the registry. Loading is a pure
// judgement of the control plane's report: it holds no cluster credential and
// writes nothing.
func Load(contributions []Contribution, options ValidationOptions) *Registry {
	registry := &Registry{
		byID:     make(map[string]int, len(contributions)),
		refusals: make([]Refusal, 0),
	}
	for _, contribution := range contributions {
		if !isActivePhase(contribution.Phase) {
			registry.refuse(contribution, "", "组件阶段为 "+phaseOf(contribution.Phase)+"，其应用不出现")
			continue
		}
		manifest, err := DecodeManifest(contribution.Manifest)
		if err != nil {
			registry.refuse(contribution, manifest.ID, err.Error())
			continue
		}
		if err := manifest.Validate(options); err != nil {
			registry.refuse(contribution, manifest.ID, err.Error())
			continue
		}
		if _, exists := registry.byID[manifest.ID]; exists {
			registry.refuse(contribution, manifest.ID,
				"应用标识 "+manifest.ID+" 已被另一个已安装扩展声明")
			continue
		}
		registry.byID[manifest.ID] = len(registry.entries)
		registry.entries = append(registry.entries, Entry{
			Manifest:  manifest,
			Extension: contribution.Extension,
			Component: contribution.Component,
			Digest:    contribution.Digest,
			Surfaces:  []string{SurfaceLauncher, SurfaceDock},
		})
	}
	sort.SliceStable(registry.entries, func(left, right int) bool {
		if registry.entries[left].Name != registry.entries[right].Name {
			return registry.entries[left].Name < registry.entries[right].Name
		}
		return registry.entries[left].ID < registry.entries[right].ID
	})
	registry.byID = make(map[string]int, len(registry.entries))
	for index, entry := range registry.entries {
		registry.byID[entry.ID] = index
	}
	return registry
}

// Load reads the source and builds the registry.
func LoadFrom(ctx context.Context, source Source, options ValidationOptions) (*Registry, error) {
	contributions, err := source.Contributions(ctx)
	if err != nil {
		return nil, fmt.Errorf("apphost: read the installed applications: %w", err)
	}
	return Load(contributions, options), nil
}

// Apps lists the loaded applications, ordered by name. Both the launcher and
// the dock read this one list: an application declared once appears on both,
// and one that is absent or invalid appears on neither (DASH-C-30).
func (r *Registry) Apps() []Entry {
	return append([]Entry(nil), r.entries...)
}

// App resolves one application by identity.
func (r *Registry) App(id string) (Entry, bool) {
	index, found := r.byID[id]
	if !found {
		return Entry{}, false
	}
	return r.entries[index], true
}

// Refusals lists the contributions that produced no entry.
func (r *Registry) Refusals() []Refusal {
	return append([]Refusal(nil), r.refusals...)
}

// Origins lists the origins of the loaded applications. It is the only set the
// console's `frame-src` may name (KOSD-C-75).
func (r *Registry) Origins() []string {
	origins := make([]string, 0, len(r.entries))
	seen := make(map[string]bool, len(r.entries))
	for _, entry := range r.entries {
		origin := entry.Origin()
		if origin == "" || seen[origin] {
			continue
		}
		seen[origin] = true
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	return origins
}

// ResolveDeepLink resolves a path to the application that declared it. A path
// no installed application declares resolves to nothing: the desktop creates
// no window for it (DASH-C-30).
func (r *Registry) ResolveDeepLink(path string) (Entry, error) {
	if path == "" {
		return Entry{}, ErrNoDeepLink
	}
	// The longest declared prefix wins, so a nested link cannot be captured by
	// a shallower application.
	best := Entry{}
	bestLength := 0
	for _, entry := range r.entries {
		for _, link := range entry.DeepLinks {
			if path == link || strings.HasPrefix(path, strings.TrimSuffix(link, "/")+"/") {
				if len(link) > bestLength {
					best, bestLength = entry, len(link)
				}
			}
		}
	}
	if bestLength == 0 {
		return Entry{}, ErrNoDeepLink
	}
	return best, nil
}

// refuse records a contribution that produced no entry.
func (r *Registry) refuse(contribution Contribution, app, reason string) {
	r.refusals = append(r.refusals, Refusal{
		Extension: contribution.Extension,
		Component: contribution.Component,
		App:       app,
		Reason:    reason,
	})
}

// isActivePhase reports a component whose `App` contribution is served. An
// unknown or missing phase is not an active one: a contribution is served only
// while its component is reported ready or degraded (KOSD-C-28).
func isActivePhase(phase string) bool {
	return phase == PhaseReady || phase == PhaseDegraded
}

// phaseOf renders an empty phase for a message.
func phaseOf(phase string) string {
	if phase == "" {
		return "未知"
	}
	return phase
}

// MemorySource is an in-process source: the caller already holds the installed
// contributions, which is how an asset origin or a test is wired.
type MemorySource []Contribution

// Contributions returns the held contributions.
func (m MemorySource) Contributions(context.Context) ([]Contribution, error) {
	return []Contribution(m), nil
}
