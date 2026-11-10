package lab

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// VariantLabel names the variant a race's pod runs.
const VariantLabel = "sentinel.karimzakzouk.github.io/variant"

const (
	// readyWithin is how long a run's copy has to become ready, and its
	// replay to finish once it is.
	readyWithin = time.Minute
	// roundPods is how many of a race's pods run at once: three blocks of
	// a baseline, one candidate and a control, fewer blocks when there are
	// more candidates, so a race stays within the Lab's quota.
	roundPods = 9
)

// holdFor is how long a run's copy must stay ready, without restarting.
var holdFor = 20 * time.Second

// Traffic is what each run sends its copy once the copy is ready: reads
// traffic capture saw production answer, and writes it kept with bodies.
type Traffic struct {
	Requests []v1alpha1.ReplayRequest
	Writes   []v1alpha1.WriteReplay
	// ServerErrors are forgiven where the world's datastores are empty, so
	// a read may fail for want of data; a read not answered at all is not.
	ServerErrors bool
}

// Variant is one version of the target a race starts in every block.
type Variant struct {
	// Name is the contender's name in the race.
	Name string
	// Slug names its pods.
	Slug string
	Pod  *corev1.Pod
}

// DeletionControl is the failing copy with every container replaced by one
// that only waits: it starts, keeps the copy's probes, and does nothing the
// workload does. A fix must be told apart from it.
func DeletionControl(failing *corev1.Pod) *corev1.Pod {
	pod := failing.DeepCopy()
	pod.Spec.InitContainers = nil
	// The probe image runs as root by default; Lab copies are restricted to
	// non-root, so without an explicit user the deletion control never starts
	// and each race block waits the full readiness timeout.
	const nonRoot int64 = 65532
	if pod.Spec.SecurityContext == nil {
		pod.Spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	pod.Spec.SecurityContext.RunAsNonRoot = ptr.To(true)
	pod.Spec.SecurityContext.RunAsUser = ptr.To(nonRoot)
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		c.Image, c.Command, c.Args, c.Lifecycle = Image, nil, []string{"pause"}, nil
		if c.SecurityContext == nil {
			c.SecurityContext = &corev1.SecurityContext{}
		}
		c.SecurityContext.RunAsNonRoot = ptr.To(true)
		c.SecurityContext.RunAsUser = ptr.To(nonRoot)
	}
	return pod
}

type racePod struct {
	block   int
	variant string
	name    string
	created time.Time
	ready   time.Time
	// replay is the pod replaying traffic to the copy, once it started.
	replay string
	// writeReplay is the pod replaying writes to the copy, once it started.
	writeReplay string
	ok          *bool
}

// RunRace runs blocks of the baseline and every variant, each block's pods
// started together in random order, a few blocks at a time, until the race
// decides or maxBlocks have run. It returns the blocks run.
func (p *Prover) RunRace(ctx context.Context, ns string, built *Built, dns *corev1.PodDNSConfig, baseline *corev1.Pod, variants []Variant, traffic Traffic, race *Race, maxBlocks int) (int, error) {
	core := p.Client.CoreV1()
	all := append([]Variant{{Name: "baseline", Slug: "base", Pod: baseline}}, variants...)
	stem := baseline.Labels[CopyOf]
	if stem == "" {
		stem = baseline.Name
	}
	blocks := 0
	roundBlocks := max(1, roundPods/len(all))
	for blocks < maxBlocks {
		var pods []*racePod
		for b := blocks; b < min(blocks+roundBlocks, maxBlocks); b++ {
			order := append([]Variant(nil), all...)
			rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] }) //nolint:gosec // randomizes start order, not a secret
			for _, v := range order {
				pod := p.seal(gated(v.Pod, built.Gate), built.Allow)
				pod.Name = fmt.Sprintf("%s-%s-%d", baseline.Name, v.Slug, b)
				pod.Labels[VariantLabel] = v.Slug
				pod.Spec.DNSPolicy, pod.Spec.DNSConfig = corev1.DNSNone, dns
				if _, err := core.Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
					return blocks, fmt.Errorf("start %s's %s: %w", stem, v.Name, err)
				}
				pods = append(pods, &racePod{block: b, variant: v.Name, name: pod.Name, created: time.Now()})
			}
		}
		err := p.watchRuns(ctx, ns, pods, traffic)
		for _, rp := range pods {
			for _, name := range []string{rp.name, rp.replay, rp.writeReplay} {
				if name != "" {
					_ = core.Pods(ns).Delete(context.WithoutCancel(ctx), name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)})
				}
			}
		}
		if err != nil {
			return blocks, err
		}
		byBlock := map[int]map[string]bool{}
		for _, rp := range pods {
			if byBlock[rp.block] == nil {
				byBlock[rp.block] = map[string]bool{}
			}
			byBlock[rp.block][rp.variant] = *rp.ok
		}
		for b := blocks; b < blocks+len(byBlock); b++ {
			outcomes := byBlock[b]
			base := outcomes["baseline"]
			delete(outcomes, "baseline")
			race.Observe(base, outcomes)
		}
		blocks += len(byBlock)
		if _, _, final := race.Outcome(blocks < maxBlocks); final {
			break
		}
	}
	return blocks, nil
}

// watchRuns waits for every run's outcome. A run succeeds once its copy has
// been ready for holdFor without a restart, and has answered the traffic
// replayed to it; it fails if the copy restarts, stops, is plainly failing,
// is not ready within readyWithin, or does not answer.
func (p *Prover) watchRuns(ctx context.Context, ns string, pods []*racePod, traffic Traffic) error {
	for {
		pending := 0
		for _, rp := range pods {
			if rp.ok != nil {
				continue
			}
			pod, err := p.Client.CoreV1().Pods(ns).Get(ctx, rp.name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("watch run %s: %w", rp.name, err)
			}
			if gateFailed(pod) {
				return fmt.Errorf("the seal did not hold for run %s", rp.name)
			}
			now := time.Now()
			state := podState(pod)
			if restarted(pod) {
				state = "restarted"
			}
			switch {
			case state == "ready":
				if rp.ready.IsZero() {
					rp.ready = now
				}
				if err := p.startReplay(ctx, ns, rp, pod, traffic); err != nil {
					return err
				}
				if err := p.startWriteReplay(ctx, ns, rp, pod, traffic); err != nil {
					return err
				}
				if now.Sub(rp.ready) < holdFor {
					break
				}
				ok, err := p.replayed(ctx, ns, rp, traffic)
				switch {
				case err != nil:
					return err
				case ok != nil:
					rp.ok = ok
				case now.Sub(rp.ready) > holdFor+readyWithin:
					return fmt.Errorf("the traffic replayed to run %s did not finish", rp.name)
				}
			case state != "starting", !rp.ready.IsZero(), now.Sub(rp.created) > readyWithin:
				rp.ok = ptr.To(false)
			}
			if rp.ok == nil {
				pending++
			}
		}
		if pending == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func restarted(pod *corev1.Pod) bool {
	for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		if cs.RestartCount > 0 {
			return true
		}
	}
	return false
}

// startReplay starts the pod that sends a ready copy its traffic, unless
// there is none, or the Lab's quota has no room for it yet.
func (p *Prover) startReplay(ctx context.Context, ns string, rp *racePod, copied *corev1.Pod, traffic Traffic) error {
	if len(traffic.Requests) == 0 || rp.replay != "" || copied.Status.PodIP == "" {
		return nil
	}
	var ports []int32
	for _, r := range traffic.Requests {
		ports = append(ports, r.Port)
	}
	pod := p.seal(replayPod(ns, rp.name+"-replay", copied.Status.PodIP, traffic.Requests), podAllow(copied.Status.PodIP, ports...))
	_, err := p.Client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	switch {
	case err == nil:
		rp.replay = pod.Name
	case apierrors.IsForbidden(err) && strings.Contains(err.Error(), "exceeded quota"):
	default:
		return fmt.Errorf("replay traffic to run %s: %w", rp.name, err)
	}
	return nil
}

// startWriteReplay starts the pod that sends a ready copy its captured
// writes, unless there is none, or the Lab's quota has no room for it yet.
func (p *Prover) startWriteReplay(ctx context.Context, ns string, rp *racePod, copied *corev1.Pod, traffic Traffic) error {
	if len(traffic.Writes) == 0 || rp.writeReplay != "" || copied.Status.PodIP == "" {
		return nil
	}
	var ports []int32
	for _, w := range traffic.Writes {
		ports = append(ports, w.Port)
	}
	pod := p.seal(writeReplayPod(ns, rp.name+"-write-replay", copied.Status.PodIP, traffic.Writes), podAllow(copied.Status.PodIP, ports...))
	_, err := p.Client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	switch {
	case err == nil:
		rp.writeReplay = pod.Name
	case apierrors.IsForbidden(err) && strings.Contains(err.Error(), "exceeded quota"):
	default:
		return fmt.Errorf("write replay traffic to run %s: %w", rp.name, err)
	}
	return nil
}

// replayed reports whether a run's copy answered its traffic: true without
// traffic, nil while a replay has not finished.
func (p *Prover) replayed(ctx context.Context, ns string, rp *racePod, traffic Traffic) (*bool, error) {
	if len(traffic.Requests) == 0 && len(traffic.Writes) == 0 {
		return ptr.To(true), nil
	}
	readOK := true
	if len(traffic.Requests) > 0 {
		if rp.replay == "" {
			return nil, nil
		}
		pod, err := p.Client.CoreV1().Pods(ns).Get(ctx, rp.replay, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("watch the replay to run %s: %w", rp.name, err)
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
		case corev1.PodFailed:
			return nil, fmt.Errorf("the replay to run %s failed: %s", rp.name, podState(pod))
		default:
			return nil, nil
		}
		codes, err := replayCodes(terminationMessage(pod), len(traffic.Requests))
		if err != nil {
			return nil, fmt.Errorf("the replay to run %s: %w", rp.name, err)
		}
		readOK = answered(codes, traffic.ServerErrors)
	}
	writeOK := true
	if len(traffic.Writes) > 0 {
		if rp.writeReplay == "" {
			return nil, nil
		}
		pod, err := p.Client.CoreV1().Pods(ns).Get(ctx, rp.writeReplay, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("watch the write replay to run %s: %w", rp.name, err)
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
		case corev1.PodFailed:
			return nil, fmt.Errorf("the write replay to run %s failed: %s", rp.name, podState(pod))
		default:
			return nil, nil
		}
		codes, err := writeReplayCodes(terminationMessage(pod), len(traffic.Writes))
		if err != nil {
			return nil, fmt.Errorf("the write replay to run %s: %w", rp.name, err)
		}
		writeOK = writesAnswered(codes, traffic.Writes, traffic.ServerErrors)
	}
	return ptr.To(readOK && writeOK), nil
}

// answered reports whether a copy answered every replayed read, without a
// server error unless those are forgiven.
func answered(codes []int, serverErrors bool) bool {
	for _, c := range codes {
		if c == 0 || c >= 500 && !serverErrors {
			return false
		}
	}
	return true
}

// replayPod sends every request to the copy at ip at once, and writes each
// one's status, 0 when nothing answered, as a line of its termination
// message. The URLs are arguments, never part of the script.
func replayPod(ns, name, ip string, requests []v1alpha1.ReplayRequest) *corev1.Pod {
	const script = `set +e; : > /dev/termination-log; i=0
for u in "$@"; do
  ( s=$(wget -S -q -O /dev/null -T 5 "$u" 2>&1 | grep -o 'HTTP/[0-9.]* [0-9][0-9][0-9]' | sed -n '$p'); c=${s##* }; echo "$i ${c:-0}" >> /dev/termination-log ) &
  i=$((i+1))
done
wait`
	args := []string{"sh", "-c", script, "replay"}
	for _, r := range requests {
		args = append(args, "http://"+hostPort(ip, int(r.Port))+r.Path)
	}
	pod := labPod(ns, name, nil, args)
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
	}
	return pod
}

// replayCodes reads the replay's lines: each request's status, by index.
func replayCodes(message string, n int) ([]int, error) {
	codes, seen := make([]int, n), make([]bool, n)
	for _, line := range strings.Split(strings.TrimSpace(message), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		i, err1 := strconv.Atoi(f[0])
		c, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || i < 0 || i >= n {
			continue
		}
		codes[i], seen[i] = c, true
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("it reported nothing for request %d", i)
		}
	}
	return codes, nil
}

// withPorts gives each request without a port the copy's first declared
// one, and drops those it has none for.
func withPorts(requests []v1alpha1.ReplayRequest, copied *corev1.Pod) []v1alpha1.ReplayRequest {
	var first int32
	for _, c := range copied.Spec.Containers {
		for _, p := range c.Ports {
			if first == 0 && (p.Protocol == "" || p.Protocol == corev1.ProtocolTCP) {
				first = p.ContainerPort
			}
		}
	}
	var out []v1alpha1.ReplayRequest
	for _, r := range requests {
		if r.Port == 0 {
			r.Port = first
		}
		if r.Port != 0 {
			out = append(out, r)
		}
	}
	return out
}
