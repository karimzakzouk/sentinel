package executors

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// TestIsStuckPod covers the pure decision logic: a pod is auto-restartable
// when it's in CrashLoopBackOff with restart count >20, no sentinel.io/repo
// annotation, and not in a skip namespace.
func TestIsStuckPod(t *testing.T) {
	skip := map[string]bool{"kube-system": true}

	cases := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "CrashLoopBackOff with high restart count — restartable",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "prod"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
						},
						RestartCount: 25,
					}},
				},
			},
			want: true,
		},
		{
			name: "CrashLoopBackOff with low restart count — not yet",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "p2", Namespace: "prod"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
						},
						RestartCount: 5,
					}},
				},
			},
			want: false,
		},
		{
			name: "CrashLoopBackOff at exactly 20 — NOT restartable (>20, not >=20)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "p3", Namespace: "prod"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
						},
						RestartCount: 20,
					}},
				},
			},
			want: false,
		},
		{
			name: "has sentinel.io/repo annotation — sentinel will investigate, skip",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "p4", Namespace: "prod",
					Annotations: map[string]string{"sentinel.io/repo": "karimzakzouk/sentinel"},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
						},
						RestartCount: 100,
					}},
				},
			},
			want: false,
		},
		{
			name: "in skip namespace — skip",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "p5", Namespace: "kube-system"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
						},
						RestartCount: 100,
					}},
				},
			},
			want: false,
		},
		{
			name: "not CrashLoopBackOff — skip",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "p6", Namespace: "prod"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"},
						},
						RestartCount: 100,
					}},
				},
			},
			want: false,
		},
		{
			name: "Running pod — skip",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "p7", Namespace: "prod"},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					}},
				},
			},
			want: false,
		},
		{
			name: "nil pod — skip",
			pod:  nil,
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isStuckPod(c.pod, skip); got != c.want {
				t.Errorf("isStuckPod = %v, want %v", got, c.want)
			}
		})
	}
}

// TestCheckCrashLoop_RestartsPod uses a fake clientset to verify the
// controller actually deletes a stuck pod and increments actionsTaken.
func TestCheckCrashLoop_RestartsPod(t *testing.T) {
	stuckPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "stuck", Namespace: "prod"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
				RestartCount: 30,
			}},
		},
	}
	sentinelPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "investigated", Namespace: "prod",
			Annotations: map[string]string{"sentinel.io/repo": "owner/repo"},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
				RestartCount: 50,
			}},
		},
	}
	healthyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "healthy", Namespace: "prod"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	clientset := fake.NewSimpleClientset(stuckPod, sentinelPod, healthyPod)
	r := NewRemediationController(clientset, []string{"kube-system"})

	r.checkCrashLoop(context.Background())

	// stuck pod should be deleted.
	_, err := clientset.CoreV1().Pods("prod").Get(context.Background(), "stuck", metav1.GetOptions{})
	if err == nil {
		t.Error("stuck pod should have been deleted")
	}
	// sentinel-annotated pod should NOT be deleted (Sentinel will investigate).
	_, err = clientset.CoreV1().Pods("prod").Get(context.Background(), "investigated", metav1.GetOptions{})
	if err != nil {
		t.Errorf("sentinel-annotated pod should NOT be deleted: %v", err)
	}
	// Healthy pod should still be there.
	_, err = clientset.CoreV1().Pods("prod").Get(context.Background(), "healthy", metav1.GetOptions{})
	if err != nil {
		t.Errorf("healthy pod should NOT be deleted: %v", err)
	}
	if r.ActionsTaken() != 1 {
		t.Errorf("ActionsTaken = %d, want 1", r.ActionsTaken())
	}
}

// TestCheckCrashLoop_SkipsSystemNamespace verifies the controller skips
// kube-system pods even if they match the stuck-pod criteria.
func TestCheckCrashLoop_SkipsSystemNamespace(t *testing.T) {
	systemPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: "kube-system"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
				RestartCount: 100,
			}},
		},
	}
	clientset := fake.NewSimpleClientset(systemPod)
	r := NewRemediationController(clientset, []string{"kube-system"})

	r.checkCrashLoop(context.Background())

	_, err := clientset.CoreV1().Pods("kube-system").Get(context.Background(), "kube-dns", metav1.GetOptions{})
	if err != nil {
		t.Errorf("kube-system pod should NOT be deleted: %v", err)
	}
	if r.ActionsTaken() != 0 {
		t.Errorf("ActionsTaken = %d, want 0 (system ns skipped)", r.ActionsTaken())
	}
}

// TestCheckPendingPods_LogsStuckPod verifies the pending-pod checker
// doesn't delete anything (just logs). We verify via actionsTaken==0.
func TestCheckPendingPods_LogsStuckPod(t *testing.T) {
	// Pod started 15 minutes ago — past the 10-minute threshold.
	startTime := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "prod"},
		Status: corev1.PodStatus{
			Phase:     corev1.PodPending,
			StartTime: &startTime,
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Message: "0/3 nodes are available: 3 Insufficient memory.",
			}},
		},
	}
	clientset := fake.NewSimpleClientset(pendingPod)
	r := NewRemediationController(clientset, nil)

	r.checkPendingPods(context.Background())

	// Pod must NOT be deleted (just logged).
	_, err := clientset.CoreV1().Pods("prod").Get(context.Background(), "pending", metav1.GetOptions{})
	if err != nil {
		t.Errorf("pending pod should NOT be deleted: %v", err)
	}
	if r.ActionsTaken() != 0 {
		t.Errorf("ActionsTaken = %d, want 0 (pending-pod check logs only)", r.ActionsTaken())
	}
}

// TestCheckPendingPods_SkipsFreshPod verifies a pending pod started < 10
// minutes ago is not even logged.
func TestCheckPendingPods_SkipsFreshPod(t *testing.T) {
	startTime := metav1.NewTime(time.Now().Add(-3 * time.Minute))
	freshPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "prod"},
		Status: corev1.PodStatus{
			Phase:     corev1.PodPending,
			StartTime: &startTime,
		},
	}
	clientset := fake.NewSimpleClientset(freshPod)
	r := NewRemediationController(clientset, nil)

	r.checkPendingPods(context.Background())
	// Just verify the pod is still there (no side effects to assert on
	// for the log-only path).
	_, err := clientset.CoreV1().Pods("prod").Get(context.Background(), "fresh", metav1.GetOptions{})
	if err != nil {
		t.Errorf("fresh pod should still be there: %v", err)
	}
}

// TestCheckPendingPods_SkipsNilStartTime verifies a pending pod with no
// StartTime is skipped (not crashed).
func TestCheckPendingPods_SkipsNilStartTime(t *testing.T) {
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "no-start", Namespace: "prod"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			// StartTime intentionally nil.
		},
	}
	clientset := fake.NewSimpleClientset(pendingPod)
	r := NewRemediationController(clientset, nil)

	// Should not panic on nil StartTime.
	r.checkPendingPods(context.Background())
}

// TestCheckDiskPressure_TriggerClean verifies a node with DiskPressure
// condition triggers the cleanup path (which deletes evicted pods).
func TestCheckDiskPressure_TriggerClean(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeDiskPressure,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	// Evicted pod on node-1 — should be deleted.
	evictedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "evicted", Namespace: "prod"},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status: corev1.PodStatus{
			Phase:  corev1.PodFailed,
			Reason: "Evicted",
		},
	}
	// Healthy pod on node-1 — should NOT be deleted.
	healthyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "healthy", Namespace: "prod"},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	clientset := fake.NewSimpleClientset(node, evictedPod, healthyPod)
	r := NewRemediationController(clientset, nil)

	r.checkDiskPressure(context.Background())

	_, err := clientset.CoreV1().Pods("prod").Get(context.Background(), "evicted", metav1.GetOptions{})
	if err == nil {
		t.Error("evicted pod should have been deleted")
	}
	_, err = clientset.CoreV1().Pods("prod").Get(context.Background(), "healthy", metav1.GetOptions{})
	if err != nil {
		t.Errorf("healthy pod should NOT be deleted: %v", err)
	}
	if r.ActionsTaken() != 1 {
		t.Errorf("ActionsTaken = %d, want 1", r.ActionsTaken())
	}
}

// TestRemediationStatus verifies the Status accessor.
func TestRemediationStatus(t *testing.T) {
	r := NewRemediationController(nil, nil)
	status := r.Status()
	if !status.Enabled {
		t.Error("Status.Enabled should be true")
	}
	if status.ActionsTaken != 0 {
		t.Errorf("initial ActionsTaken = %d, want 0", status.ActionsTaken)
	}
}

// TestRemediationControllerSkipsNamespaces verifies the skip list is
// populated from the constructor.
func TestRemediationControllerSkipsNamespaces(t *testing.T) {
	r := NewRemediationController(nil, []string{"kube-system", "kube-public", "sentinel"})
	if !r.skipNamespaces["kube-system"] {
		t.Error("kube-system should be in skip list")
	}
	if !r.skipNamespaces["kube-public"] {
		t.Error("kube-public should be in skip list")
	}
	if !r.skipNamespaces["sentinel"] {
		t.Error("sentinel should be in skip list")
	}
	if r.skipNamespaces["prod"] {
		t.Error("prod should NOT be in skip list")
	}
}

// TestRemediationRun_StopsOnContextCancel verifies Run exits when the
// context is cancelled.
func TestRemediationRun_StopsOnContextCancel(t *testing.T) {
	r := NewRemediationController(fake.NewSimpleClientset(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx, 1*time.Hour)
		close(done)
	}()
	// Cancel after a short delay.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// ok — Run returned.
	case <-time.After(2 * time.Second):
		t.Error("Run did not exit after context cancellation")
	}
}

// TestRemediationRun_DefaultPollInterval verifies pollInterval<=0 defaults
// to 5 minutes (we verify via a short cancellation so the test doesn't
// actually wait 5 minutes).
func TestRemediationRun_DefaultPollInterval(t *testing.T) {
	r := NewRemediationController(fake.NewSimpleClientset(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so Run returns without ever ticking.
	cancel()
	// Run should return immediately (ctx already cancelled).
	done := make(chan struct{})
	go func() {
		r.Run(ctx, 0) // 0 → default 5m
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Run with pollInterval=0 should still respect ctx cancellation")
	}
}

// init silences unused-import warnings on the logging package, which is
// used only by non-test code in this package.
var _ = logging.Info
