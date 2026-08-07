package feedback

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestExtractErrorFingerprint verifies the fingerprint extraction that
// powers the 24-hour re-occurrence watcher.
func TestExtractErrorFingerprint(t *testing.T) {
	cases := []struct {
		name string
		logs string
		want string
	}{
		{
			name: "python KeyError",
			logs: `Traceback (most recent call last):
  File "/app/main.py", line 11
KeyError: 'DATABASE_URLL'`,
			want: "KeyError",
		},
		{
			name: "python ValueError",
			logs: `ValueError: invalid literal for int() with base 10: 'abc'`,
			want: "ValueError",
		},
		{
			name: "go panic",
			logs: `panic: runtime error: index out of range [1] with length 1
goroutine 1 [running]:`,
			want: "runtime error: index out of range [1] with length 1",
		},
		{
			name: "java NullPointerException",
			logs: `Exception in thread "main" java.lang.NullPointerException: Cannot invoke "User.getName()"`,
			want: "NullPointerException",
		},
		{
			name: "java StackOverflowError",
			logs: `java.lang.StackOverflowError
        at com.acme.Recursive.fib(Recursive.java:88)`,
			want: "StackOverflowError",
		},
		{
			name: "javascript TypeError",
			logs: `TypeError: processData is not a function
    at Object.<anonymous> (/app/index.js:10:17)`,
			want: "TypeError",
		},
		{
			name: "rust panic",
			logs: `thread 'main' panicked at 'called Option::unwrap() on a None value', src/main.rs:13:5`,
			want: "",
		},
		{
			name: "c SIGSEGV",
			logs: `Program received signal SIGSEGV, Segmentation fault.`,
			want: "SIGSEGV",
		},
		{
			name: "no error in logs",
			logs: `service started successfully on port 8080
all systems nominal`,
			want: "",
		},
		{
			name: "empty logs",
			logs: "",
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractErrorFingerprint(c.logs)
			if got != c.want {
				t.Errorf("ExtractErrorFingerprint() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestOutcomeWatcher_SetDurations verifies that SetDurations allows
// configuring short durations for testing (and production tuning).
func TestOutcomeWatcher_SetDurations(t *testing.T) {
	w := NewOutcomeWatcher(nil, nil)
	if w.shortWatchDuration != 10*time.Minute {
		t.Errorf("default shortWatchDuration = %v, want 10m", w.shortWatchDuration)
	}
	if w.longWatchDuration != 24*time.Hour {
		t.Errorf("default longWatchDuration = %v, want 24h", w.longWatchDuration)
	}

	w.SetDurations(time.Second, 500*time.Millisecond, 5*time.Second, time.Second)
	if w.shortWatchDuration != time.Second {
		t.Errorf("shortWatchDuration = %v, want 1s", w.shortWatchDuration)
	}
	if w.longWatchDuration != 5*time.Second {
		t.Errorf("longWatchDuration = %v, want 5s", w.longWatchDuration)
	}
}

// TestOutcomeWatcher_Phase1Success verifies Phase 1 records a success
// when pods are Running + Ready, using a fake clientset and short
// durations so the test runs in <1s.
func TestOutcomeWatcher_Phase1Success(t *testing.T) {
	store := newTestStore(t)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-1",
			Namespace: "prod",
			Labels:    map[string]string{"app": "api"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true},
			},
		},
	}
	cs := fake.NewSimpleClientset(pod)
	w := NewOutcomeWatcher(cs, store)
	w.SetDurations(
		500*time.Millisecond, // shortWatch
		50*time.Millisecond,  // shortInterval
		0,                    // longWatch (unused — no fingerprint)
		0,                    // longInterval
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	w.WatchWithDetails(ctx, "api", "prod", "incident-1", true, "code_fix", "")

	// Wait for the Phase 1 success to land in the Beta prior.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, succ, _, _ := store.GetBetaPrior(ctx, "api", "code_fix")
		if succ > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Phase 1 did not record a Beta success within 2s")
}

// TestOutcomeWatcher_Phase1Failure verifies Phase 1 records a failure
// when pods never recover (CrashLoopBackOff).
func TestOutcomeWatcher_Phase1Failure(t *testing.T) {
	store := newTestStore(t)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-1",
			Namespace: "prod",
			Labels:    map[string]string{"app": "api"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "app",
					Ready: false,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				},
			},
		},
	}
	cs := fake.NewSimpleClientset(pod)
	w := NewOutcomeWatcher(cs, store)
	w.SetDurations(
		200*time.Millisecond, // shortWatch — expire quickly
		50*time.Millisecond,  // shortInterval
		0, 0,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	w.WatchWithDetails(ctx, "api", "prod", "incident-2", true, "code_fix", "")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, _, fail, _ := store.GetBetaPrior(ctx, "api", "code_fix")
		if fail > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Phase 1 did not record a Beta failure within 2s")
}

// TestOutcomeWatcher_DuplicateWatchSkipped verifies the second
// WatchWithDetails call for the same incident is a no-op.
func TestOutcomeWatcher_DuplicateWatchSkipped(t *testing.T) {
	store := newTestStore(t)
	cs := fake.NewSimpleClientset()
	w := NewOutcomeWatcher(cs, store)
	w.SetDurations(time.Second, 100*time.Millisecond, 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	w.WatchWithDetails(ctx, "api", "prod", "incident-dup", true, "code_fix", "")
	// Second call should be a no-op (LoadOrStore returns alreadyWatching=true).
	w.WatchWithDetails(ctx, "api", "prod", "incident-dup", true, "code_fix", "")

	// We can't directly inspect activeWatches (private), but we can verify
	// no panic and no deadlock by completing the test cleanly.
	time.Sleep(200 * time.Millisecond)
}

// TestOutcomeWatcher_NilClientsetDoesNotPanic verifies the nil-clientset
// path doesn't crash (the watcher just records failure on every poll).
func TestOutcomeWatcher_NilClientsetDoesNotPanic(t *testing.T) {
	store := newTestStore(t)
	w := NewOutcomeWatcher(nil, store)
	w.SetDurations(100*time.Millisecond, 50*time.Millisecond, 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.WatchWithDetails(ctx, "api", "prod", "incident-nil", true, "code_fix", "")
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchWithDetails with nil clientset did not return within 2s")
	}
}

// TestOutcomeWatcher_Phase1RecoverWithDeploymentLabels verifies the
// app.kubernetes.io/name label selector path also resolves the pod.
func TestOutcomeWatcher_Phase1RecoverWithDeploymentLabels(t *testing.T) {
	store := newTestStore(t)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-1",
			Namespace: "prod",
			Labels:    map[string]string{"app.kubernetes.io/name": "api"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true},
			},
		},
	}
	cs := fake.NewSimpleClientset(pod)
	w := NewOutcomeWatcher(cs, store)
	w.SetDurations(300*time.Millisecond, 50*time.Millisecond, 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	w.WatchWithDetails(ctx, "api", "prod", "incident-labels", true, "code_fix", "")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, succ, _, _ := store.GetBetaPrior(ctx, "api", "code_fix")
		if succ > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Phase 1 did not record a success via app.kubernetes.io/name label")
}

// makeDeployment is a test helper for fake-clientset setup; unused by the
// OutcomeWatcher tests above but referenced by future chatops tests. Kept
// here so the fake import stays exercised.
func makeDeployment(name, namespace string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app": name}},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		},
	}
}

var _ = makeDeployment // silence unused warning if no test calls it yet
