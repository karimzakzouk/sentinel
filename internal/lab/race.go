package lab

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// VariantLabel names the variant a race's pod runs.
const VariantLabel = "sentinel.karimzakzouk.github.io/variant"

const (
	// readyWithin is how long a run's copy has to become ready.
	readyWithin = time.Minute
	// holdFor is how long it must then stay ready, without restarting.
	holdFor = 20 * time.Second
	// roundPods is how many of a race's pods run at once: three blocks of
	// a baseline, one candidate and a control, fewer blocks when there are
	// more candidates, so a race stays within the Lab's quota.
	roundPods = 9
)

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
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		c.Image, c.Command, c.Args, c.Lifecycle = Image, nil, []string{"pause"}, nil
	}
	return pod
}

type racePod struct {
	block   int
	variant string
	name    string
	created time.Time
	ready   time.Time
	ok      *bool
}

// RunRace runs blocks of the baseline and every variant, each block's pods
// started together in random order, a few blocks at a time, until the race
// decides or maxBlocks have run. It returns the blocks run.
func (p *Prover) RunRace(ctx context.Context, ns string, built *Built, dns *corev1.PodDNSConfig, baseline *corev1.Pod, variants []Variant, race *Race, maxBlocks int) (int, error) {
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
				pod := gated(v.Pod, built.Gate)
				pod.Name = fmt.Sprintf("%s-%s-%d", baseline.Name, v.Slug, b)
				pod.Labels[VariantLabel] = v.Slug
				pod.Spec.DNSPolicy, pod.Spec.DNSConfig = corev1.DNSNone, dns
				if _, err := core.Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
					return blocks, fmt.Errorf("start %s's %s: %w", stem, v.Name, err)
				}
				pods = append(pods, &racePod{block: b, variant: v.Name, name: pod.Name, created: time.Now()})
			}
		}
		err := p.watchRuns(ctx, ns, pods)
		for _, rp := range pods {
			_ = core.Pods(ns).Delete(context.WithoutCancel(ctx), rp.name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)})
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
// been ready for holdFor without a restart; it fails if the copy restarts,
// stops, is plainly failing, or is not ready within readyWithin.
func (p *Prover) watchRuns(ctx context.Context, ns string, pods []*racePod) error {
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
			case state == "ready" && rp.ready.IsZero():
				rp.ready = now
			case state == "ready" && now.Sub(rp.ready) >= holdFor:
				rp.ok = ptr.To(true)
			case state != "ready" && state != "starting", !rp.ready.IsZero() && state != "ready",
				rp.ready.IsZero() && now.Sub(rp.created) > readyWithin:
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
