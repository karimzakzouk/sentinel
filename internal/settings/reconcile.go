package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// Reconcile watches the Settings object and applies each new generation to
// ConfigMaps. It runs until ctx ends.
func Reconcile(ctx context.Context, client kubernetes.Interface, dyn dynamic.Interface, t Targets, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	var lastGen int64 = -1
	apply := func(s *v1alpha1.SentinelSettings) {
		if s == nil || s.Generation == lastGen {
			return
		}
		// Already applied this generation (for example after a controller
		// restart that the previous apply caused): refresh status only when
		// installed components may have changed.
		if s.Status.ObservedGeneration == s.Generation {
			lastGen = s.Generation
			inst := probe(ctx, client, t)
			if inst.Actor != s.Status.Installed.Actor || inst.Lab != s.Status.Installed.Lab ||
				inst.Fixer != s.Status.Installed.Fixer || inst.Capture != s.Status.Installed.Capture ||
				inst.CaptureMITM != s.Status.Installed.CaptureMITM ||
				inst.CaptureMITMTransparent != s.Status.Installed.CaptureMITMTransparent ||
				inst.NodeAgent != s.Status.Installed.NodeAgent {
				_ = setStatus(ctx, dyn, t.AgentNamespace, s, inst, s.Status.Message)
			}
			return
		}
		got, err := ApplyWithDynamic(ctx, client, dyn, t, s.Spec)
		if err != nil {
			log.Error("could not apply SentinelSettings", "error", err)
			_ = setStatus(ctx, dyn, t.AgentNamespace, s, got.Installed, "apply failed: "+err.Error())
			return
		}
		lastGen = s.Generation
		if err := setStatus(ctx, dyn, t.AgentNamespace, s, got.Installed, got.Message); err != nil {
			log.Error("could not update SentinelSettings status", "error", err)
			return
		}
		log.Info("applied SentinelSettings", "generation", s.Generation, "message", got.Message)
	}
	for ctx.Err() == nil {
		if err := watchOnce(ctx, dyn, t.AgentNamespace, apply); err != nil && ctx.Err() == nil {
			log.Error("SentinelSettings watch ended", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func watchOnce(ctx context.Context, dyn dynamic.Interface, ns string, apply func(*v1alpha1.SentinelSettings)) error {
	list, err := dyn.Resource(Resource).Namespace(ns).List(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + Name})
	if err != nil {
		return err
	}
	for i := range list.Items {
		apply(decode(&list.Items[i]))
	}
	w, err := dyn.Resource(Resource).Namespace(ns).Watch(ctx, metav1.ListOptions{
		FieldSelector:   "metadata.name=" + Name,
		ResourceVersion: list.GetResourceVersion(),
	})
	if err != nil {
		return err
	}
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.ResultChan():
			if !ok {
				return fmt.Errorf("watch closed")
			}
			switch ev.Type {
			case watch.Added, watch.Modified:
				u, _ := ev.Object.(*unstructured.Unstructured)
				apply(decode(u))
			}
		}
	}
}

func decode(u *unstructured.Unstructured) *v1alpha1.SentinelSettings {
	if u == nil {
		return nil
	}
	raw, err := json.Marshal(u.Object)
	if err != nil {
		return nil
	}
	var s v1alpha1.SentinelSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	return &s
}

func setStatus(ctx context.Context, dyn dynamic.Interface, ns string, s *v1alpha1.SentinelSettings, installed v1alpha1.InstalledComponents, message string) error {
	now := metav1.Now()
	status := v1alpha1.SentinelSettingsStatus{
		ObservedGeneration: s.Generation,
		AppliedAt:          &now,
		Message:            message,
		Installed:          installed,
	}
	body, err := json.Marshal(map[string]any{"status": status})
	if err != nil {
		return err
	}
	_, err = dyn.Resource(Resource).Namespace(ns).Patch(ctx, Name, types.MergePatchType, body, metav1.PatchOptions{FieldManager: "sentinel"}, "status")
	return err
}
