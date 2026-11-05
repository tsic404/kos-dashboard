package dock

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/tsic404/kosd/api"
	"github.com/tsic404/kosd/api/v1beta1"
	"github.com/tsic404/kosd/pkg/reconcile"
	"github.com/tsic404/kosd/pkg/state"
	"github.com/tsic404/kosd/pkg/write"
)

// The controller identity and the declarative reference it registers (C-19,
// C-20).
const (
	// EntryControllerID names the desktop entry controller contribution.
	EntryControllerID = "desktop-entry"
	// ApplicationKind is the kernel class an entry points at.
	ApplicationKind = "Application"
	// ApplicationRefPath is the entry spec field that carries the declarative
	// reference to the application the entry opens (C-19). The kernel follows
	// object references only, so the controller mirrors spec.application into
	// this field; spec.application stays the user-written source.
	ApplicationRefPath = "spec.applicationRef"
)

// The Reconciled reasons this controller writes (C-07).
const (
	// ReasonEntryReady marks an entry whose application opens.
	ReasonEntryReady = "ApplicationReady"
	// ReasonApplicationNotReady marks an entry kept because its application is
	// installed but not usable; the entry is never deleted for that (C-30).
	ReasonApplicationNotReady = "ApplicationNotReady"
	// ReasonEntryUnresolved marks an entry the controller cannot resolve.
	ReasonEntryUnresolved = "EntryUnresolved"
)

// entryResource is this package's own object class.
var entryResource = schema.GroupVersionResource{
	Group: APIGroup, Version: EntryVersion, Resource: EntryResource,
}

// applicationResource is the kernel class the reference names (C-19).
var applicationResource = schema.GroupVersionResource{
	Group: api.Group, Version: v1beta1.Version, Resource: "applications",
}

// Config wires the entry controller to the cluster it serves.
type Config struct {
	// Dynamic reads this package's objects and the applications they open.
	Dynamic dynamic.Interface
	// Writer writes as the extension controller (C-20); its scope carries this
	// package's API group and nothing else.
	Writer *write.Writer
}

// Controller is the desktop entry lifecycle controller (C-20). It keeps the
// entries of installed applications, removes the entries of an application
// that is going away, and registers each entry's application as the
// declarative reference the kernel reads (C-19). It writes this package's
// object class alone: kernel objects and other extensions' objects are read,
// never written.
//
// The host registers it with the declared write scope, delivers requests for
// the kernel classes its ReadScope names, and calls Sync before serving the
// class again (C-19, C-28).
type Controller struct {
	dynamic dynamic.Interface
	writer  *write.Writer
}

// NewController builds the entry controller.
func NewController(config Config) *Controller {
	return &Controller{dynamic: config.Dynamic, writer: config.Writer}
}

// Reconcile runs one round. An entry request reconciles that entry; an
// application request reconciles every entry that names the application, so an
// uninstall reaches its entries as soon as it starts.
func (c *Controller) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	switch request.Kind {
	case EntryKind:
		return c.reconcileEntry(ctx, request)
	case ApplicationKind:
		return c.reconcileApplication(ctx, request)
	default:
		return reconcile.Result{}, nil
	}
}

// Sync reconciles every stored entry once. A component that becomes active runs
// it before it serves the entry class again, so the entries of applications
// uninstalled while it was not serving are removed first (C-28, C-30).
func (c *Controller) Sync(ctx context.Context) error {
	list, err := c.dynamic.Resource(entryResource).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("list desktop entries: %w", err)
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for index := range list.Items {
		object := &list.Items[index]
		requests = append(requests, reconcile.Request{Kind: EntryKind, Namespace: object.GetNamespace(), Name: object.GetName()})
	}
	sort.Slice(requests, func(left, right int) bool {
		return requests[left].Namespace+"/"+requests[left].Name < requests[right].Namespace+"/"+requests[right].Name
	})
	for _, request := range requests {
		if _, err := c.Reconcile(ctx, request); err != nil {
			return err
		}
	}
	return nil
}

// reconcileEntry keeps or removes one entry from the state of the application
// it names.
func (c *Controller) reconcileEntry(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	entry, err := c.dynamic.Resource(entryResource).Namespace(request.Namespace).Get(ctx, request.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("read desktop entry %s/%s: %w", request.Namespace, request.Name, err)
	}
	spec, err := SpecOf(entry)
	if err != nil {
		return c.write(ctx, entry, nil, metav1.ConditionFalse, ReasonEntryUnresolved, err.Error())
	}
	if spec.Application == "" {
		return c.write(ctx, entry, nil, metav1.ConditionFalse, ReasonEntryUnresolved, "条目未声明所属应用")
	}
	application, err := c.dynamic.Resource(applicationResource).Namespace(request.Namespace).Get(ctx, spec.Application, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// The application is gone: the entry it opened is removed with it
		// (C-30). This also sweeps entries stored while the class was not
		// served, before the desktop lists them again.
		return reconcile.Result{}, c.remove(ctx, entry)
	case err != nil:
		return reconcile.Result{}, fmt.Errorf("read application %s/%s: %w", request.Namespace, spec.Application, err)
	}
	if application.GetDeletionTimestamp() != nil {
		// The application entered uninstallation: removing the entry releases
		// the uninstall its reference holds (C-19, C-30).
		return reconcile.Result{}, c.remove(ctx, entry)
	}
	reference := map[string]any{
		"apiVersion": api.Group + "/" + v1beta1.Version,
		"kind":       ApplicationKind,
		"name":       spec.Application,
	}
	if reason := UnavailableReason(application, spec.Application); reason != "" {
		return c.write(ctx, entry, reference, metav1.ConditionFalse, ReasonApplicationNotReady, reason)
	}
	return c.write(ctx, entry, reference, metav1.ConditionTrue, ReasonEntryReady, "应用 "+spec.Application+" 已就绪")
}

// reconcileApplication reconciles every entry that names one application, so a
// round of the application's own controller reaches its entries.
func (c *Controller) reconcileApplication(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	list, err := c.dynamic.Resource(entryResource).Namespace(request.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("list desktop entries: %w", err)
	}
	for index := range list.Items {
		object := &list.Items[index]
		spec, err := SpecOf(object)
		if err != nil || spec.Application != request.Name {
			continue
		}
		round := reconcile.Request{Kind: EntryKind, Namespace: object.GetNamespace(), Name: object.GetName()}
		if _, err := c.reconcileEntry(ctx, round); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

// write stores the entry's declarative reference and its Reconciled condition
// (C-07, C-19). A nil reference names no application, so the field is dropped.
// Both fields travel in one write because one field manager owns them: an apply
// that omitted the reference would remove it.
func (c *Controller) write(ctx context.Context, entry *unstructured.Unstructured, reference map[string]any, status metav1.ConditionStatus, reason, message string) (reconcile.Result, error) {
	generation := entry.GetGeneration()
	observed := entryStatus{}
	if raw, found, err := unstructured.NestedMap(entry.Object, "status"); err != nil {
		return reconcile.Result{}, fmt.Errorf("read the status of %s: %w", entry.GetName(), err)
	} else if found {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &observed); err != nil {
			return reconcile.Result{}, fmt.Errorf("decode the status of %s: %w", entry.GetName(), err)
		}
	}
	condition := state.Condition(api.ConditionReconciled, status, generation, reason, message)
	conditions := state.Set(append([]metav1.Condition(nil), observed.Conditions...), condition)
	stored, _, err := unstructured.NestedMap(entry.Object, "spec", "applicationRef")
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("read the application reference of %s: %w", entry.GetName(), err)
	}
	if reflect.DeepEqual(conditions, observed.Conditions) && observed.ObservedGeneration == generation &&
		reflect.DeepEqual(stored, reference) {
		// The round observed what the stored entry already says; a write would
		// only move the resource version (C-06).
		return reconcile.Result{}, nil
	}
	observed.ObservedGeneration = generation
	observed.Conditions = conditions
	encoded, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&observed)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("encode the status of %s: %w", entry.GetName(), err)
	}
	document := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": APIGroup + "/" + EntryVersion,
		"kind":       EntryKind,
		"metadata":   map[string]any{"name": entry.GetName(), "namespace": entry.GetNamespace()},
		"status":     encoded,
	}}
	if reference != nil {
		document.Object["spec"] = map[string]any{"applicationRef": reference}
	}
	if _, err := c.writer.Apply(ctx, write.SubjectExtensionReconciler, write.Session{}, write.PartObject, document); err != nil {
		return reconcile.Result{}, fmt.Errorf("write desktop entry %s: %w", entry.GetName(), err)
	}
	return reconcile.Result{}, nil
}

// remove deletes one entry as the extension controller. An entry already gone
// is the state the round wanted, so it is not an error.
func (c *Controller) remove(ctx context.Context, entry *unstructured.Unstructured) error {
	err := c.writer.Delete(ctx, write.SubjectExtensionReconciler, write.Session{}, write.RefOf(entry))
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove desktop entry %s: %w", entry.GetName(), err)
	}
	return nil
}

// entryStatus is the status shape the controller keeps for one entry.
type entryStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}
