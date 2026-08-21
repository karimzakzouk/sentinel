package detection

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/errorparse"
	"github.com/karimzakzouk/sentinel/internal/logging"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// ErrorFingerprint uniquely identifies an error for deduplication.
type ErrorFingerprint struct {
	Service   string
	Namespace string
	ErrorType string
}

// Detector is the autonomous detection layer. It streams pod logs and
// watches pod status, producing triggers when error patterns or
// pod-status transitions (CrashLoopBackOff, OOMKilled, ImagePullBackOff)
// are observed. Only pods with the sentinel.io/repo annotation (or whose
// owning Deployment / StatefulSet carries it) are watched.
type Detector struct {
	clientset kubernetes.Interface
	handler   IncidentHandler
	alertURL  string

	mu         sync.Mutex
	recentSeen map[ErrorFingerprint]time.Time

	streams   map[string]context.CancelFunc
	streamsMu sync.Mutex

	// discoveredPods dedupes the discovery log so the watch firing 10x
	// per second during pod startup doesn't flood the log.
	discoveredPods map[string]time.Time
	discoveredMu   sync.Mutex

	cooldown  time.Duration
	tailLines int64

	skipNamespaces map[string]bool
}

// New returns a Detector with the default 5-minute cooldown, 100-line tail,
// and the kube-system / kube-public / kube-node-lease skip list.
func New(clientset kubernetes.Interface, handler IncidentHandler, alertURL string) *Detector {
	return NewWithConfig(clientset, handler, alertURL, 5*time.Minute, 100, nil)
}

// NewWithConfig returns a Detector with the given knobs. Pass cooldown=0 or
// tailLines=0 to keep the defaults (5m / 100). Pass nil skipNamespaces to
// keep the default skip list; pass an explicit slice (including an empty
// one) to override.
func NewWithConfig(clientset kubernetes.Interface, handler IncidentHandler, alertURL string, cooldown time.Duration, tailLines int64, skipNamespaces []string) *Detector {
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	if tailLines <= 0 {
		tailLines = 100
	}
	skip := map[string]bool{}
	if len(skipNamespaces) > 0 {
		for _, ns := range skipNamespaces {
			skip[ns] = true
		}
	} else {
		for _, ns := range []string{
			"kube-system", "kube-public", "kube-node-lease",
		} {
			skip[ns] = true
		}
	}
	return &Detector{
		clientset:      clientset,
		handler:        handler,
		alertURL:       alertURL,
		recentSeen:     make(map[ErrorFingerprint]time.Time),
		streams:        make(map[string]context.CancelFunc),
		discoveredPods: make(map[string]time.Time),
		cooldown:       cooldown,
		tailLines:      tailLines,
		skipNamespaces: skip,
	}
}

// Run starts the detection layer and blocks until ctx is cancelled.
func (d *Detector) Run(ctx context.Context) {
	logging.Info("detector: starting autonomous detection layer")
	go d.cleanupLoop(ctx)
	d.watchPods(ctx)
}

// watchPods lists every namespace once at startup, fans out a per-namespace
// watcher, then watches for newly-added namespaces.
func (d *Detector) watchPods(ctx context.Context) {
	if d.clientset == nil {
		logging.Warn("detector: nil clientset — watcher disabled")
		<-ctx.Done()
		return
	}
	namespaces, err := d.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		logging.Error("detector: failed to list namespaces", "err", err)
		return
	}
	for _, ns := range namespaces.Items {
		go d.watchPodsInNamespace(ctx, ns.Name)
	}
	go d.watchNamespaces(ctx)
	<-ctx.Done()
}

// watchNamespaces opens a Watch on the namespace resource so newly-created
// namespaces get a pod-watcher goroutine.
func (d *Detector) watchNamespaces(ctx context.Context) {
	watcher, err := d.clientset.CoreV1().Namespaces().Watch(ctx, metav1.ListOptions{})
	if err != nil {
		logging.Error("detector: failed to watch namespaces", "err", err)
		return
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return
			}
			ns, ok := event.Object.(*corev1.Namespace)
			if !ok || ns.Status.Phase != corev1.NamespaceActive {
				continue
			}
			go d.watchPodsInNamespace(ctx, ns.Name)
		}
	}
}

// watchPodsInNamespace lists all pods in the namespace once, then opens a
// Watch starting from the list's resourceVersion. Skipped namespaces are
// ignored entirely. On watch close, the loop sleeps briefly and re-lists.
func (d *Detector) watchPodsInNamespace(ctx context.Context, namespace string) {
	if d.skipNamespaces[namespace] {
		return
	}

watchLoop:
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pods, err := d.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			time.Sleep(30 * time.Second)
			continue
		}

		for i := range pods.Items {
			d.handlePod(ctx, &pods.Items[i])
		}

		watcher, err := d.clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
			ResourceVersion: pods.ResourceVersion,
		})
		if err != nil {
			time.Sleep(15 * time.Second)
			continue
		}

		for {
			select {
			case <-ctx.Done():
				watcher.Stop()
				return
			case event, ok := <-watcher.ResultChan():
				if !ok {
					watcher.Stop()
					time.Sleep(5 * time.Second)
					continue watchLoop
				}
				pod, ok := event.Object.(*corev1.Pod)
				if !ok {
					continue
				}
				// Cancel the log-stream goroutine on pod deletion so the
				// retry loop doesn't keep hitting "pod not found" forever.
				if event.Type == watch.Deleted {
					d.stopLogStream(string(pod.UID))
					continue
				}
				d.handlePod(ctx, pod)
			}
		}
	}
}

// handlePod resolves the sentinel.io/repo annotation (on the pod or by
// walking its owner chain), logs the pod once for discoverability, then
// checks pod status and (if Running) opens a log stream.
func (d *Detector) handlePod(ctx context.Context, pod *corev1.Pod) {
	annotations := pod.Annotations
	repo, hasRepo := "", false
	if annotations != nil {
		repo, hasRepo = annotations["sentinel.io/repo"]
	}

	if !hasRepo {
		repo = d.lookupRepoFromOwner(ctx, pod)
		if repo != "" {
			hasRepo = true
		}
	}

	if !hasRepo {
		return
	}

	// Dedupe the discovery log: the watch fires on every status change
	// (10+ times per second during startup), which would otherwise flood
	// the log.
	d.discoveredMu.Lock()
	podUID := string(pod.UID)
	if _, seen := d.discoveredPods[podUID]; seen {
		d.discoveredMu.Unlock()
	} else {
		d.discoveredPods[podUID] = time.Now()
		d.discoveredMu.Unlock()
		logging.Info("detector: found annotated pod",
			"namespace", pod.Namespace, "name", pod.Name,
			"phase", pod.Status.Phase, "repo", repo)
	}

	d.checkPodStatus(ctx, pod, repo)

	if pod.Status.Phase == corev1.PodRunning {
		d.startLogStream(ctx, pod, repo)
	}
}

// lookupRepoFromOwner walks the pod's owner chain (ReplicaSet → Deployment,
// or StatefulSet directly) looking for the sentinel.io/repo annotation on
// either the owner's metadata or its pod-template metadata. This handles
// the common case where users annotate the Deployment instead of the pod
// template.
func (d *Detector) lookupRepoFromOwner(ctx context.Context, pod *corev1.Pod) string {
	for _, ownerRef := range pod.OwnerReferences {
		switch ownerRef.Kind {
		case "ReplicaSet":
			rs, err := d.clientset.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, ownerRef.Name, metav1.GetOptions{})
			if err != nil {
				logging.Error("detector: failed to get ReplicaSet",
					"namespace", pod.Namespace, "name", ownerRef.Name, "err", err)
				continue
			}
			if repo, ok := rs.Annotations["sentinel.io/repo"]; ok {
				return repo
			}
			if repo, ok := rs.Spec.Template.Annotations["sentinel.io/repo"]; ok {
				return repo
			}
			for _, rsOwner := range rs.OwnerReferences {
				if rsOwner.Kind == "Deployment" {
					if repo := d.getDeploymentRepo(ctx, pod.Namespace, rsOwner.Name); repo != "" {
						return repo
					}
				}
			}
		case "Deployment":
			if repo := d.getDeploymentRepo(ctx, pod.Namespace, ownerRef.Name); repo != "" {
				return repo
			}
		case "StatefulSet":
			sts, err := d.clientset.AppsV1().StatefulSets(pod.Namespace).Get(ctx, ownerRef.Name, metav1.GetOptions{})
			if err == nil {
				if repo, ok := sts.Annotations["sentinel.io/repo"]; ok {
					return repo
				}
				if repo, ok := sts.Spec.Template.Annotations["sentinel.io/repo"]; ok {
					return repo
				}
			}
		}
	}
	return ""
}

// getDeploymentRepo fetches a Deployment and checks both its metadata
// annotations and pod template annotations for sentinel.io/repo.
func (d *Detector) getDeploymentRepo(ctx context.Context, namespace, name string) string {
	deploy, err := d.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	if repo, ok := deploy.Annotations["sentinel.io/repo"]; ok {
		return repo
	}
	if repo, ok := deploy.Spec.Template.Annotations["sentinel.io/repo"]; ok {
		return repo
	}
	return ""
}

// checkPodStatus fires triggers for the pod-status transitions Sentinel
// treats as incidents: CrashLoopBackOff, LastTerminationState with non-zero
// exit code, OOMKilled, and ImagePullBackOff. Each trigger is subject to
// the per-fingerprint cooldown.
func (d *Detector) checkPodStatus(ctx context.Context, pod *corev1.Pod, repo string) {
	service := d.getServiceFromPod(pod)
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			if d.shouldTrigger(pod.Namespace, service, "CrashLoopBackOff") {
				logging.Info("detector: CrashLoopBackOff",
					"namespace", pod.Namespace, "name", pod.Name)
				logs := d.fetchPodLogs(ctx, pod)
				reason := fmt.Sprintf("CrashLoopBackOff: %s", cs.State.Waiting.Message)
				d.triggerIncident(pod, repo, reason, logs)
			}
			return
		}
		// LastTerminationState fires before CrashLoopBackOff — without it
		// Sentinel waits 2-3 minutes for CrashLoopBackOff before reacting.
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.ExitCode != 0 {
			reason := cs.LastTerminationState.Terminated.Reason
			if reason == "" {
				reason = fmt.Sprintf("ExitCode=%d", cs.LastTerminationState.Terminated.ExitCode)
			}
			if d.shouldTrigger(pod.Namespace, service, reason) {
				logging.Info("detector: container crashed",
					"namespace", pod.Namespace, "name", pod.Name,
					"reason", reason, "restart_count", cs.RestartCount)
				logs := d.fetchPodLogs(ctx, pod)
				d.triggerIncident(pod, repo, fmt.Sprintf("Container crash: %s", reason), logs)
			}
			return
		}
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
			if d.shouldTrigger(pod.Namespace, service, "OOMKilled") {
				logging.Info("detector: OOMKilled",
					"namespace", pod.Namespace, "name", pod.Name)
				logs := d.fetchPodLogs(ctx, pod)
				d.triggerIncident(pod, repo, "OOMKilled — container exceeded memory limit", logs)
			}
			return
		}
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "ImagePullBackOff" {
			if d.shouldTrigger(pod.Namespace, service, "ImagePullBackOff") {
				logging.Info("detector: ImagePullBackOff",
					"namespace", pod.Namespace, "name", pod.Name)
				reason := fmt.Sprintf("ImagePullBackOff: %s", cs.State.Waiting.Message)
				d.triggerIncident(pod, repo, reason, "")
			}
			return
		}
	}
}

// startLogStream opens a streaming log request for the pod and runs
// streamPodLogs in a goroutine. One stream per pod UID; the streams map
// lets stopLogStream cancel a specific pod's stream on watch.Deleted.
func (d *Detector) startLogStream(ctx context.Context, pod *corev1.Pod, repo string) {
	podUID := string(pod.UID)

	d.streamsMu.Lock()
	if _, exists := d.streams[podUID]; exists {
		d.streamsMu.Unlock()
		return
	}
	streamCtx, cancel := context.WithCancel(ctx)
	d.streams[podUID] = cancel
	d.streamsMu.Unlock()

	go func() {
		// Recover here (not in streamPodLogs) so a panic on the streaming
		// path can't silently kill the goroutine and leave the pod
		// unmonitored until restart.
		defer func() {
			if r := recover(); r != nil {
				logging.Info("detector: log stream recovered",
					"namespace", pod.Namespace, "name", pod.Name, "r", r)
			}
		}()
		defer func() {
			d.streamsMu.Lock()
			delete(d.streams, podUID)
			d.streamsMu.Unlock()
		}()
		logging.Info("detector: log stream started",
			"namespace", pod.Namespace, "name", pod.Name)
		d.streamPodLogs(streamCtx, pod, repo)
	}()
}

// stopLogStream cancels the log-stream goroutine for a pod UID. Called on
// watch.Deleted so the retry loop doesn't keep hitting "pod not found".
func (d *Detector) stopLogStream(podUID string) {
	d.streamsMu.Lock()
	cancel, ok := d.streams[podUID]
	if ok {
		delete(d.streams, podUID)
	}
	d.streamsMu.Unlock()
	if ok {
		cancel()
		logging.Info("detector: log stream stopped", "pod_uid", podUID)
	}
}

// StopAllLogStreams cancels every active log stream. Call on shutdown.
func (d *Detector) StopAllLogStreams() {
	d.streamsMu.Lock()
	defer d.streamsMu.Unlock()
	for uid, cancel := range d.streams {
		cancel()
		delete(d.streams, uid)
	}
	logging.Info("detector: stopped all log streams")
}

// streamPodLogs opens a Follow: true log request and scans lines as they
// arrive. Python tracebacks are buffered until the error-type line at the
// bottom (so the investigator gets "KeyError" not "unknown_error"); other
// error patterns fire on the first matching line, with the next 20 lines
// captured for context.
func (d *Detector) streamPodLogs(ctx context.Context, pod *corev1.Pod, repo string) {
	containerName := ""
	if len(pod.Spec.Containers) > 0 {
		containerName = pod.Spec.Containers[0].Name
	}

	opts := &corev1.PodLogOptions{
		Follow:    true,
		Container: containerName,
		TailLines: ptr(d.tailLines),
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		req := d.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts)
		stream, err := req.Stream(ctx)
		if err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
				logging.Info("detector: log stream ended, pod no longer exists",
					"namespace", pod.Namespace, "name", pod.Name)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

		// Traceback buffer: holds lines until the error-type line at the
		// end is reached, so the trigger fires with the correct error
		// type instead of "unknown_error".
		var tracebackBuffer strings.Builder
		var inTraceback bool
		var tracebackLines int

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				stream.Close()
				return
			default:
			}

			line := scanner.Text()

			if strings.Contains(line, "Traceback (most recent call last)") {
				inTraceback = true
				tracebackBuffer.Reset()
				tracebackLines = 0
				tracebackBuffer.WriteString(line + "\n")
				tracebackLines++
				continue
			}

			if inTraceback {
				tracebackBuffer.WriteString(line + "\n")
				tracebackLines++

				errorType := d.extractErrorType(line)
				isEndOfTraceback := errorType != "unknown_error" && errorType != ""

				if !isEndOfTraceback {
					if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") &&
						(strings.Contains(line, "Error") || strings.Contains(line, "Exception") ||
							strings.Contains(line, "panic") || strings.Contains(line, "fatal")) {
						errorType = d.extractErrorType(line)
						isEndOfTraceback = errorType != "unknown_error" && errorType != ""
					}
				}

				if isEndOfTraceback || tracebackLines > 50 {
					inTraceback = false
					logs := tracebackBuffer.String()
					tracebackBuffer.Reset()

					service := d.getServiceFromPod(pod)
					if d.shouldTrigger(pod.Namespace, service, errorType) {
						reason := fmt.Sprintf("Error detected in pod logs: %s", errorType)
						logging.Error("detector: log error",
							"namespace", pod.Namespace, "name", pod.Name, "error_type", errorType)
						d.triggerIncident(pod, repo, reason, logs)
					}
				}
				continue
			}

			if d.isErrorLine(line) {
				errorType := d.extractErrorType(line)
				service := d.getServiceFromPod(pod)

				if d.shouldTrigger(pod.Namespace, service, errorType) {
					var buf strings.Builder
					buf.WriteString(line + "\n")
					for i := 0; i < 20 && scanner.Scan(); i++ {
						buf.WriteString(scanner.Text() + "\n")
					}

					logs := buf.String()
					reason := fmt.Sprintf("Error detected in pod logs: %s", errorType)
					logging.Error("detector: log error",
						"namespace", pod.Namespace, "name", pod.Name, "error_type", errorType)
					d.triggerIncident(pod, repo, reason, logs)
				}
			}
		}

		stream.Close()

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// isErrorLine reports whether the line matches any known error pattern.
// Delegates to the shared errorparse package to avoid pattern drift.
func (d *Detector) isErrorLine(line string) bool {
	return errorparse.IsErrorLine(line)
}

// extractErrorType returns the error-type name from a line, or
// "unknown_error" if no pattern matches.
func (d *Detector) extractErrorType(line string) string {
	if t := errorparse.ExtractErrorType(line); t != "" {
		return t
	}
	return "unknown_error"
}

// shouldTrigger reports whether the (namespace, service, errorType)
// fingerprint is allowed to fire. The first call records the timestamp;
// subsequent calls within d.cooldown are suppressed.
func (d *Detector) shouldTrigger(namespace, service, errorType string) bool {
	fp := ErrorFingerprint{Service: service, Namespace: namespace, ErrorType: errorType}

	d.mu.Lock()
	defer d.mu.Unlock()

	if lastSeen, exists := d.recentSeen[fp]; exists {
		if time.Since(lastSeen) < d.cooldown {
			return false
		}
	}
	d.recentSeen[fp] = time.Now()
	return true
}

// triggerIncident invokes the configured handler (in a goroutine) and, if
// alertURL is set, the alert sink.
func (d *Detector) triggerIncident(pod *corev1.Pod, repo, reason, podLogs string) {
	service := d.getServiceFromPod(pod)

	if d.handler != nil {
		go d.handler(service, pod.Namespace, reason, podLogs)
	}

	if d.alertURL != "" {
		go d.sendAlert(service, pod.Namespace, reason, podLogs)
	} else {
		logging.Info("detector: trigger (no alert URL configured)",
			"namespace", pod.Namespace, "service", service, "reason", reason)
	}
}

// sendAlert forwards the trigger to the configured alert sink. Currently
// structured-logged; a real implementation would POST to alertURL.
func (d *Detector) sendAlert(service, namespace, reason, podLogs string) {
	logging.Info("detector: alert sent",
		"namespace", namespace, "service", service, "reason", reason)
}

// getServiceFromPod resolves the service name from pod labels, falling back
// to the pod name with the trailing "-<replica>" segment stripped.
func (d *Detector) getServiceFromPod(pod *corev1.Pod) string {
	if name, ok := pod.Labels["app.kubernetes.io/name"]; ok {
		return name
	}
	if name, ok := pod.Labels["app"]; ok {
		return name
	}
	name := pod.Name
	if idx := strings.LastIndex(name, "-"); idx > 0 {
		name = name[:idx]
	}
	return name
}

// fetchPodLogs returns up to 8KB of previous-container logs (the crash
// output) for the pod; if that's empty or contains the K8s placeholder, it
// falls back to current logs.
func (d *Detector) fetchPodLogs(ctx context.Context, pod *corev1.Pod) string {
	limitBytes := int64(8192)
	containerName := ""
	if len(pod.Spec.Containers) > 0 {
		containerName = pod.Spec.Containers[0].Name
	}

	opts := &corev1.PodLogOptions{
		Previous:   true,
		LimitBytes: &limitBytes,
		Container:  containerName,
	}
	if logs, err := d.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Do(ctx).Raw(); err == nil {
		s := string(logs)
		if strings.TrimSpace(s) != "" && !strings.Contains(s, "unable to retrieve container logs") {
			return s
		}
	}

	opts.Previous = false
	if logs, err := d.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Do(ctx).Raw(); err == nil {
		return string(logs)
	}

	return ""
}

// cleanupLoop drops recentSeen entries older than 10 minutes and
// discoveredPods entries older than 1 hour. Both maps grow without bound
// otherwise.
func (d *Detector) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.mu.Lock()
			for fp, seen := range d.recentSeen {
				if time.Since(seen) > 10*time.Minute {
					delete(d.recentSeen, fp)
				}
			}
			d.mu.Unlock()

			d.discoveredMu.Lock()
			for uid, seen := range d.discoveredPods {
				if time.Since(seen) > time.Hour {
					delete(d.discoveredPods, uid)
				}
			}
			d.discoveredMu.Unlock()
		}
	}
}

// ptr returns a pointer to v — a generic helper for K8s option structs.
func ptr[T any](v T) *T { return &v }
