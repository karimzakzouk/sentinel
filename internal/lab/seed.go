package lab

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/capture"
	"github.com/karimzakzouk/sentinel/internal/model"
)

const (
	seedMax       = 20
	seedKeyPrefix = "sentinel:lab:seed:"
)

// SeedFidelity says what captured writes will do to empty datastores before write replay.
func SeedFidelity(plan *Plan, samples []capture.BodySample) []string {
	if len(samples) == 0 || len(plan.Empty) == 0 {
		return nil
	}
	var out []string
	redisLike := false
	for _, ref := range plan.Empty {
		_, _, _, engine, ok := emptyDatastore(plan, ref)
		if !ok {
			continue
		}
		switch engine {
		case "redis", "valkey":
			redisLike = true
		default:
			out = append(out, fmt.Sprintf("%s runs an empty %s: seeding from captured bodies is not run yet", ref, engine))
		}
	}
	if redisLike {
		n := min(len(samples), seedMax)
		out = append(out, fmt.Sprintf("before write replay, empty redis or valkey datastores are seeded with %d captured write bodies as %s0..%d",
			n, seedKeyPrefix, n-1))
	}
	return out
}

// SeedEmptyDatastores runs one-shot seed pods against empty redis or valkey copies.
func (p *Prover) SeedEmptyDatastores(ctx context.Context, ns string, built *Built, plan *Plan, samples []capture.BodySample) ([]string, error) {
	notes := SeedFidelity(plan, samples)
	if len(samples) == 0 {
		return notes, nil
	}
	bodies := seedBodies(samples)
	var created []string
	defer func() {
		if len(created) == 0 {
			return
		}
		core := p.Client.CoreV1()
		ctx := context.WithoutCancel(ctx)
		for _, name := range created {
			_ = core.Pods(ns).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0))})
		}
	}()
	for _, ref := range plan.Empty {
		image, host, port, engine, ok := emptyDatastore(plan, ref)
		if !ok || engine != "redis" && engine != "valkey" {
			continue
		}
		ip := serviceIP(built, ref.Name)
		if ip == "" {
			return notes, fmt.Errorf("no Service for empty datastore %s", ref.Name)
		}
		name := "lab-seed-" + ref.Name
		pod := seedRedisPod(ns, name, image, host, port, bodies)
		pod = p.seal(pod, append(built.Allow, podAllow(ip, port)...))
		if _, err := p.Client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return notes, fmt.Errorf("start seed for %s: %w", ref, err)
		}
		created = append(created, name)
	}
	for _, name := range created {
		if err := p.waitPodSucceeded(ctx, ns, name, time.Minute); err != nil {
			return notes, err
		}
	}
	return notes, nil
}

func seedBodies(samples []capture.BodySample) []string {
	var out []string
	for _, s := range samples {
		if len(out) >= seedMax {
			break
		}
		body := s.Body
		if body == "" {
			body = s.ResponseBody
		}
		if body == "" {
			continue
		}
		out = append(out, body)
	}
	return out
}

func emptyDatastore(plan *Plan, ref model.ObjectRef) (image, host string, port int32, engine string, ok bool) {
	key := ref.Kind + "." + ref.Name
	for _, pod := range plan.Pods {
		if pod.Labels[CopyOf] != key {
			continue
		}
		eng, found := datastoreEngine(pod.Spec)
		if !found || len(pod.Spec.Containers) == 0 {
			return "", "", 0, "", false
		}
		port = 6379
		for _, cp := range pod.Spec.Containers[0].Ports {
			if cp.ContainerPort > 0 {
				port = cp.ContainerPort
				break
			}
		}
		return pod.Spec.Containers[0].Image, ref.Name, port, eng, true
	}
	return "", "", 0, "", false
}

func serviceIP(built *Built, name string) string {
	for _, s := range built.Services {
		if s.Name == name && s.Spec.ClusterIP != "" && s.Spec.ClusterIP != "None" {
			return s.Spec.ClusterIP
		}
	}
	return ""
}

const seedRedisScript = `set -e
host="$1"; port="$2"; shift 2
i=0
for body in "$@"; do
  redis-cli -h "$host" -p "$port" SET "sentinel:lab:seed:$i" "$body" >/dev/null
  i=$((i+1))
done
echo "seeded $i keys" > /dev/termination-log
`

func seedRedisPod(ns, name, image, host string, port int32, bodies []string) *corev1.Pod {
	args := []string{"sh", "-c", seedRedisScript, "seed", host, fmt.Sprint(port)}
	args = append(args, bodies...)
	pod := labPod(ns, name, nil, args)
	pod.Spec.Containers[0].Image = image
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
	}
	return pod
}

func (p *Prover) waitPodSucceeded(ctx context.Context, ns, name string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		pod, err := p.Client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return nil
		case corev1.PodFailed:
			return fmt.Errorf("seed pod %s failed: %s", name, strings.TrimSpace(terminationMessage(pod)))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("seed pod %s did not finish within %s", name, within)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
