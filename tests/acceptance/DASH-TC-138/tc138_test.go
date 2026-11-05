// Package dashtc138 verifies the desktop entry lifecycle controller (DASH-AC-66,
// DASH-TC-138): uninstalling an application removes its entries and leaves the
// other applications' entries and data alone; an installed application that is
// not ready keeps its entry and the reason it cannot open; the entries are the
// declarative references the kernel lists for their application while
// kos-dashboard serves the class (C-19); and a component that becomes active
// again sweeps the entries of applications uninstalled while it was away before
// the class serves them (C-28, C-30).
package dashtc138

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/tsic404/kos-dashboard/pkg/dock"
	"github.com/tsic404/kosd/api"
	"github.com/tsic404/kosd/api/v1beta1"
	"github.com/tsic404/kosd/pkg/app"
	"github.com/tsic404/kosd/pkg/controlplane"
	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/pkg/reconcile"
	"github.com/tsic404/kosd/pkg/state"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"
)

const (
	// definitionName renders the downstream workload every installed
	// application owns.
	definitionName = "desktop-lifecycle"
	// gatedDefinition requires a capability no provider registers, so its
	// application stays installed but never becomes ready (C-11).
	gatedDefinition = "desk-gated"
	// The applications of the scenario.
	media  = "media"
	photos = "photos"
	notes  = "notes"
	draft  = "draft"
	// The entries: two for the first application, one for each of the others.
	entryDesktop = "media-desktop"
	entryDock    = "media-dock"
	entryPhotos  = "photos-entry"
	entryNotes   = "notes-entry"
	entryDraft   = "draft-entry"
	// volumeName is the volume the second application keeps its data on
	// (KOSD-F-5).
	volumeName = "photos-data"
)

// workloadTemplate renders one Deployment, so an uninstall has a downstream
// object the entry cleanup must not touch.
const workloadTemplate = `apiVersion: apps/v1
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
        - name: app
          image: example.test/app:1
`

// TestTC138UninstallingAnApplicationRemovesItsEntries walks the design scenario:
// two applications keep their entries while the first one is uninstalled, a
// third one is uninstalled while kos-dashboard does not serve the entry class,
// and the component's next activation sweeps the leftover entry before the
// class is served again.
func TestTC138UninstallingAnApplicationRemovesItsEntries(t *testing.T) {
	cluster := harness.Start(t)
	ctx := context.Background()
	cluster.InstallCRD(t, dock.APIGroup, dock.EntryVersion, dock.EntryKind, dock.EntryResource, true)
	cluster.InstallDefinition(t, harness.DefinitionWithTemplate(t, definitionName, workloadTemplate, nil))
	cluster.InstallDefinition(t, harness.Definition(t, gatedDefinition, []v1beta1.CapabilityRequirement{{
		Name: "desktop.test/missing", Version: ">=v1",
	}}))
	for _, name := range []string{media, photos, notes} {
		cluster.CreateApplication(t, name, definitionName, harness.Namespace)
	}
	cluster.CreateApplication(t, draft, gatedDefinition, harness.Namespace)
	applications := cluster.ApplicationController(t, app.Options{})
	for _, name := range []string{media, photos, notes} {
		if phase := cluster.SettleApplication(t, applications, name, 6); phase != app.PhaseReady {
			t.Fatalf("application %s phase = %q, want %q", name, phase, app.PhaseReady)
		}
	}
	// The fourth application is installed but never ready; its entry must stay
	// and carry the reason (C-30).
	cluster.ApplicationRound(t, applications, draft)
	if phase := cluster.ApplicationPhase(t, draft); phase != app.PhasePending {
		t.Fatalf("application %s phase = %q, want %q", draft, phase, app.PhasePending)
	}
	cluster.Create(t, harness.Volume(harness.Namespace, volumeName, map[string]any{
		"mobility": string(v1beta1.VolumeMobilityClusterMovable), "capacity": "1Gi",
	}))

	artifacts := extension.MemoryArtifacts{}
	installation := cluster.InstallPackage(t, "dock", dock.Declaration(), artifacts)
	result, found := installation.Component("dock")
	if !found || result.Phase != extension.PhaseReady || len(result.Problems) > 0 {
		t.Fatalf("kos-dashboard component = %+v (found %v), want Ready with no contribution problem", result, found)
	}
	// The declaration carries the controller and the reference path (C-19, C-20).
	declaration := dock.Declaration()
	if len(declaration.Contributions.Controllers) != 1 ||
		declaration.Contributions.Controllers[0].ID != dock.EntryControllerID {
		t.Fatalf("declared controllers = %+v, want the entry lifecycle controller", declaration.Contributions.Controllers)
	}
	if classes := installation.Registry().ObjectClasses(); len(classes) != 1 ||
		len(classes[0].References) != 1 || classes[0].References[0] != dock.ApplicationRefPath {
		t.Fatalf("served entry class = %+v, want the application reference path", classes)
	}
	// One class has one controller (C-20): a second contribution for it is refused.
	controllers := extension.NewControllerSet(3)
	writer := entryWriter(t, cluster)
	entryController := dock.NewController(dock.Config{Dynamic: cluster.Dynamic, Writer: writer})
	if err := controllers.Register("dock", declaration.Contributions.Controllers[0], entryController); err != nil {
		t.Fatalf("register the entry controller: %v", err)
	}
	if err := controllers.Register("other", declaration.Contributions.Controllers[0], entryController); err == nil {
		t.Fatal("a second controller registered the entry class")
	}

	control := cluster.StartControlPlane(t, func(config *controlplane.Config) {
		config.Extensions = installation.Registry()
	})
	for _, created := range []struct{ name, application string }{
		{entryDesktop, media}, {entryDock, media}, {entryPhotos, photos}, {entryNotes, notes}, {entryDraft, draft},
	} {
		createEntry(t, control, created.name, created.application)
	}
	// The lifecycle of the stored entries, one round each.
	for _, name := range entryNames(t, cluster) {
		entryRound(t, entryController, name)
	}

	// Each entry registered its application as the declarative reference the
	// kernel reads, and the entry is kept with the application's state (C-19).
	for _, created := range []struct{ name, application string }{
		{entryDesktop, media}, {entryDock, media}, {entryPhotos, photos}, {entryNotes, notes}, {entryDraft, draft},
	} {
		if reference := entryReference(t, cluster, created.name); reference != applicationReference(created.application) {
			t.Fatalf("entry %s reference = %s, want %s", created.name, reference, applicationReference(created.application))
		}
		// The user's fields keep their writer: the controller adds the reference
		// and the status only.
		if spec := entrySpec(t, cluster, created.name); value(spec, "application") != created.application ||
			value(spec, "displayName") != created.name {
			t.Fatalf("entry %s spec = %v, want the user-written fields untouched", created.name, spec)
		}
	}
	for _, name := range []string{entryDesktop, entryDock, entryPhotos, entryNotes} {
		if reconciled := entryCondition(t, cluster, name); reconciled.Status != metav1.ConditionTrue {
			t.Fatalf("entry %s Reconciled = %+v, want True", name, reconciled)
		}
	}
	// An installed but unusable application keeps its entry and the reason (C-30).
	draftEntry := entryObject(t, cluster, entryDraft)
	reconciled := entryCondition(t, cluster, entryDraft)
	if reconciled.Status != metav1.ConditionFalse || reconciled.Reason != dock.ReasonApplicationNotReady {
		t.Fatalf("entry %s Reconciled = %+v, want False/%s", entryDraft, reconciled, dock.ReasonApplicationNotReady)
	}
	if !strings.Contains(reconciled.Message, app.PhasePending) {
		t.Fatalf("entry %s reason = %q, want the application's phase", entryDraft, reconciled.Message)
	}
	if draftEntry.GetDeletionTimestamp() != nil {
		t.Fatalf("entry %s is deleted while its application is installed", entryDraft)
	}

	// The kernel's declarative-reference gate names the entries as referrers of
	// the application while kos-dashboard serves the class (C-19). A deletion
	// held by that gate reports exactly this list.
	if referrers := referrersOf(t, cluster, installation.Registry(), media); len(referrers) != 2 {
		t.Fatalf("referrers of application %s = %v, want both entries", media, referrers)
	} else {
		for _, referrer := range referrers {
			if referrer.Kind != dock.EntryKind {
				t.Fatalf("referrer %+v is not a desktop entry", referrer)
			}
		}
	}
	// The controller writes its own class alone (C-20): a kernel write through
	// the scope it runs with is refused.
	kernelWrite := writer.Delete(ctx, write.SubjectExtensionReconciler, write.Session{}, write.Ref{
		Group: api.Group, Version: v1beta1.Version, Kind: "Application", Namespace: harness.Namespace, Name: photos,
	})
	var denied *write.Denied
	if !errors.As(kernelWrite, &denied) {
		t.Fatalf("kernel write with the controller's scope = %v, want a denial", kernelWrite)
	}
	// The applications and the volume are read, never written: their stored
	// state is the same after the controller's rounds.
	photosVersion := resourceVersion(t, cluster, api.Group, v1beta1.Version, "Application", photos)
	volumeVersion := resourceVersion(t, cluster, api.Group, v1beta1.Version, "Volume", volumeName)

	// The first application enters uninstallation; the controller removes its
	// entries at once and the uninstall then completes (C-30).
	deleteApplication(t, cluster, media)
	applicationRound(t, entryController, media)
	if remaining := entryNames(t, cluster); len(remaining) != 3 {
		t.Fatalf("entries after the first uninstall = %v, want the first application's entries gone", remaining)
	}
	settleUninstall(t, cluster, applications, media)
	// The entries of the application that stays are still there, and so is its
	// volume; the uninstalled application's rendered workload is gone with it:
	// the cleanup touches entries only and leaves the uninstall's handling of
	// downstream objects alone (KOSD-F-5).
	if remaining := entryNames(t, cluster); len(remaining) != 3 {
		t.Fatalf("entries after the uninstall completed = %v, want the other applications' entries", remaining)
	}
	if !entryExists(t, cluster, entryPhotos) || !entryExists(t, cluster, entryDraft) {
		t.Fatal("an entry of an installed application was removed")
	}
	if _, err := cluster.Direct(t, api.Group, v1beta1.Version, "Volume", harness.Namespace).Get(ctx, volumeName, metav1.GetOptions{}); err != nil {
		t.Fatalf("the volume of the second application was removed: %v", err)
	}
	if _, err := cluster.Direct(t, "apps", "v1", "Deployment", harness.Namespace).Get(ctx, media, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the rendered workload of application %s survived its uninstall (err = %v)", media, err)
	}
	if referrers := referrersOf(t, cluster, installation.Registry(), media); len(referrers) != 0 {
		t.Fatalf("referrers of the uninstalled application = %v, want none", referrers)
	}

	// The third application uninstalls while kos-dashboard does not serve the
	// entry class. The stored reference does not participate in delete blocking
	// (C-19), so the uninstall completes; the entry stays in storage because no
	// controller runs.
	cluster.SetComponentDisabled(t, "dock", true)
	disabled := cluster.ExtensionState(t, artifacts)
	if result, found := disabled.Component("dock"); !found || result.Phase != extension.PhaseDisabled {
		t.Fatalf("disabled kos-dashboard component = %+v (found %v), want %q", result, found, extension.PhaseDisabled)
	}
	if referrers := referrersOf(t, cluster, disabled.Registry(), notes); len(referrers) != 0 {
		t.Fatalf("unserved class references = %v, want none", referrers)
	}
	deleteApplication(t, cluster, notes)
	settleUninstall(t, cluster, applications, notes)
	if !entryExists(t, cluster, entryNotes) {
		t.Fatal("the entry was removed while the class was not served; no controller ran")
	}

	// The component becomes active again: its first round sweeps the entries of
	// already-uninstalled applications before the class serves them (C-28).
	cluster.SetComponentDisabled(t, "dock", false)
	restored := cluster.ExtensionState(t, artifacts)
	if result, found := restored.Component("dock"); !found || result.Phase != extension.PhaseReady {
		t.Fatalf("restored kos-dashboard component = %+v (found %v), want %q", result, found, extension.PhaseReady)
	}
	if err := entryController.Sync(ctx); err != nil {
		t.Fatalf("sweep the stored entries after activation: %v", err)
	}
	if entryExists(t, cluster, entryNotes) {
		t.Fatal("the entry of the application uninstalled while disabled is still listed")
	}
	if referrers := referrersOf(t, cluster, restored.Registry(), notes); len(referrers) != 0 {
		t.Fatalf("referrers of the application uninstalled while disabled = %v, want none", referrers)
	}
	if !entryExists(t, cluster, entryPhotos) || !entryExists(t, cluster, entryDraft) {
		t.Fatal("the sweep removed an entry of an installed application")
	}

	// The controller never wrote a kernel object or the volume.
	if current := resourceVersion(t, cluster, api.Group, v1beta1.Version, "Application", photos); current != photosVersion {
		t.Fatalf("application %s resource version = %q, want %q", photos, current, photosVersion)
	}
	if current := resourceVersion(t, cluster, api.Group, v1beta1.Version, "Volume", volumeName); current != volumeVersion {
		t.Fatalf("volume %s resource version = %q, want %q", volumeName, current, volumeVersion)
	}
}

// entryWriter builds the write path the controller runs with: the declared
// scope of its contribution, which carries this package's API group alone.
func entryWriter(t *testing.T, cluster *harness.Cluster) *write.Writer {
	t.Helper()
	resources := cluster.Resources()
	resources[schema.GroupKind{Group: dock.APIGroup, Kind: dock.EntryKind}] = dock.EntryResource
	return &write.Writer{
		Dynamic:   cluster.Dynamic,
		Resources: resources,
		Scope:     dock.Declaration().Contributions.Controllers[0].Scope.Admit(),
	}
}

// entryManifest is the body the desktop page's add action submits (DASH-C-22).
func entryManifest(name, application string) map[string]any {
	return map[string]any{
		"apiVersion": dock.APIGroup + "/" + dock.EntryVersion,
		"kind":       dock.EntryKind,
		"metadata":   map[string]any{"name": name, "namespace": harness.Namespace},
		"spec": map[string]any{
			"application": application,
			"displayName": name,
			"icon":        application,
			"visibility":  dock.VisibilityEveryone,
			"order":       10,
			"source":      "manual",
		},
	}
}

// createEntry creates one entry through the page's declared add action.
func createEntry(t *testing.T, control *harness.ControlPlane, name, application string) {
	t.Helper()
	method, path := harness.PageAction(t, dock.DesktopPage(), dock.ActionAddEntry, "", harness.Namespace)
	if status, body := control.Call(t, method, path, entryManifest(name, application), nil); status != http.StatusAccepted {
		t.Fatalf("create entry %s = %d %v, want %d", name, status, body, http.StatusAccepted)
	}
}

// entryRound runs one round of the entry controller for one entry.
func entryRound(t *testing.T, controller *dock.Controller, name string) {
	t.Helper()
	if _, err := controller.Reconcile(context.Background(), reconcile.Request{
		Kind: dock.EntryKind, Namespace: harness.Namespace, Name: name,
	}); err != nil {
		t.Fatalf("reconcile desktop entry %s: %v", name, err)
	}
}

// applicationRound runs one round of the entry controller for one application,
// which reconciles every entry that names it.
func applicationRound(t *testing.T, controller *dock.Controller, name string) {
	t.Helper()
	if _, err := controller.Reconcile(context.Background(), reconcile.Request{
		Kind: dock.ApplicationKind, Namespace: harness.Namespace, Name: name,
	}); err != nil {
		t.Fatalf("reconcile entries of application %s: %v", name, err)
	}
}

// entries resolves the entry class client.
func entries(t *testing.T, cluster *harness.Cluster) dynamic.ResourceInterface {
	t.Helper()
	return cluster.ExtensionResource(dock.APIGroup, dock.EntryVersion, dock.EntryResource, harness.Namespace)
}

// entryObject reads one stored entry.
func entryObject(t *testing.T, cluster *harness.Cluster, name string) *unstructured.Unstructured {
	t.Helper()
	stored, err := entries(t, cluster).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read desktop entry %s: %v", name, err)
	}
	return stored
}

// entryExists reports whether one entry is stored.
func entryExists(t *testing.T, cluster *harness.Cluster, name string) bool {
	t.Helper()
	_, err := entries(t, cluster).Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

// entrySpec reads one stored entry's spec.
func entrySpec(t *testing.T, cluster *harness.Cluster, name string) map[string]any {
	t.Helper()
	spec, found, err := unstructured.NestedMap(entryObject(t, cluster, name).Object, "spec")
	if err != nil || !found {
		t.Fatalf("entry %s has no spec: found %v err %v", name, found, err)
	}
	return spec
}

// entryReference renders the declarative reference the entry registered.
func entryReference(t *testing.T, cluster *harness.Cluster, name string) string {
	t.Helper()
	reference, found, err := unstructured.NestedMap(entryObject(t, cluster, name).Object, "spec", "applicationRef")
	if err != nil || !found {
		t.Fatalf("entry %s carries no application reference: found %v err %v", name, found, err)
	}
	apiVersion, _ := reference["apiVersion"].(string)
	kind, _ := reference["kind"].(string)
	refName, _ := reference["name"].(string)
	return apiVersion + "/" + kind + "/" + refName
}

// applicationReference is the reference one application entry declares.
func applicationReference(application string) string {
	return api.Group + "/" + v1beta1.Version + "/" + dock.ApplicationKind + "/" + application
}

// value reads one spec field as text.
func value(spec map[string]any, field string) string {
	text, _ := spec[field].(string)
	return text
}

// entryCondition reads the entry's Reconciled condition (C-07).
func entryCondition(t *testing.T, cluster *harness.Cluster, name string) metav1.Condition {
	t.Helper()
	raw, found, err := unstructured.NestedSlice(entryObject(t, cluster, name).Object, "status", "conditions")
	if err != nil || !found {
		t.Fatalf("entry %s has no conditions: found %v err %v", name, found, err)
	}
	conditions := make([]metav1.Condition, 0, len(raw))
	for _, item := range raw {
		encoded, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var condition metav1.Condition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(encoded, &condition); err != nil {
			t.Fatalf("decode the condition of entry %s: %v", name, err)
		}
		conditions = append(conditions, condition)
	}
	condition, ok := state.Find(conditions, api.ConditionReconciled)
	if !ok {
		t.Fatalf("entry %s carries no %s condition: %+v", name, api.ConditionReconciled, conditions)
	}
	return condition
}

// entryNames lists the stored entry names in a stable order.
func entryNames(t *testing.T, cluster *harness.Cluster) []string {
	t.Helper()
	list, err := entries(t, cluster).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list desktop entries: %v", err)
	}
	names := make([]string, 0, len(list.Items))
	for index := range list.Items {
		names = append(names, list.Items[index].GetName())
	}
	sort.Strings(names)
	return names
}

// referrersOf resolves the entries that declare a reference to one application
// through the kernel's own reference gate (C-19).
func referrersOf(t *testing.T, cluster *harness.Cluster, registry *extension.Registry, application string) []v1beta1.ObjectRef {
	t.Helper()
	referrers, err := extension.Referrers(context.Background(), cluster.Dynamic, registry, write.Ref{
		Group: api.Group, Version: v1beta1.Version, Kind: dock.ApplicationKind,
		Namespace: harness.Namespace, Name: application,
	})
	if err != nil {
		t.Fatalf("resolve the referrers of application %s: %v", application, err)
	}
	return referrers
}

// deleteApplication requests the deletion of one application, which is how its
// uninstall starts (C-33).
func deleteApplication(t *testing.T, cluster *harness.Cluster, name string) {
	t.Helper()
	applications := cluster.Direct(t, api.Group, v1beta1.Version, "Application", harness.Namespace)
	if err := applications.Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete application %s: %v", name, err)
	}
}

// settleUninstall drives the application's rounds until the object is gone, so
// the case reads the state after the uninstall completed.
func settleUninstall(t *testing.T, cluster *harness.Cluster, controller *app.Controller, name string) {
	t.Helper()
	applications := cluster.Direct(t, api.Group, v1beta1.Version, "Application", harness.Namespace)
	for range 8 {
		if _, err := applications.Get(context.Background(), name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return
		}
		cluster.ApplicationRound(t, controller, name)
	}
	if _, err := applications.Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("application %s survived its uninstall (err = %v)", name, err)
	}
}

// resourceVersion reads one object's version, which changes on any write.
func resourceVersion(t *testing.T, cluster *harness.Cluster, group, version, kind, name string) string {
	t.Helper()
	stored, err := cluster.Direct(t, group, version, kind, harness.Namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read %s %s: %v", kind, name, err)
	}
	return stored.GetResourceVersion()
}
