// Package main — Alertmanager adapter.
//
// Bridges the alertmanager.Receiver to the detection.Clusterer. Implements
// alertmanager.SignalHandler by converting each alert into a detection.Signal
// and sending it to the clusterer for deduplication + grouping.
package main

import (
        "context"
        "time"

        "github.com/karimzakzouk/sentinel/internal/detection"
        "github.com/karimzakzouk/sentinel/internal/k8s"
        "github.com/karimzakzouk/sentinel/internal/logging"
)

// Compile-time assertion that alertmanagerAdapter implements alertmanager.SignalHandler.
// (We can't import alertmanager here without creating an import cycle in main
// package — but the struct shape matches the interface. The assertion is in
// main.go where alertmanager is already imported.)

// alertmanagerAdapter forwards Alertmanager firing alerts to the detection
// Clusterer as SignalLogError-type signals.
type alertmanagerAdapter struct {
        clusterer *detection.Clusterer
        k8sClient *k8s.Client
}

// HandleAlert is called by the Alertmanager Receiver for each firing alert.
// It looks up the GitHub repo via K8s annotations (if not provided in the
// alert labels) and sends the signal to the clusterer.
func (a *alertmanagerAdapter) HandleAlert(service, namespace, reason, repo, description string, labels map[string]string) {
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        if repo == "" && a.k8sClient != nil {
                annotations, _ := a.k8sClient.LookupWorkloadAnnotations(ctx, service, namespace)
                if annotations != nil {
                        repo = annotations["sentinel.io/repo"]
                }
        }

        logging.Info("alertmanager → clusterer",
                "service", service,
                "namespace", namespace,
                "reason", reason,
                "repo", repo,
        )

        a.clusterer.Send(ctx, detection.Signal{
                Type:      detection.SignalLogError,
                Service:   service,
                Namespace: namespace,
                Reason:    reason,
                Repo:      repo,
                Timestamp: time.Now(),
        })
}
