package lab

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// Built is a synthesized world running in a Lab namespace.
type Built struct {
	// DNS is the Lab DNS's address.
	DNS      string
	Services []*corev1.Service
	// States says how each copied workload's pod is doing, by workload.
	States map[string]string
}

// Build starts plan's world in sealed namespace ns, copying production
// namespace prodNS under cluster domain domain, and waits until each pod
// is ready or plainly failing.
func (p *Prover) Build(ctx context.Context, ns, prodNS, domain string, plan *Plan) (*Built, error) {
	core := p.Client.CoreV1()
	dns, err := core.Services(ns).Create(ctx, dnsService(ns), metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create the Lab DNS service: %w", err)
	}
	for _, cm := range plan.ConfigMaps {
		if _, err := core.ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return nil, fmt.Errorf("copy ConfigMap %s: %w", cm.Name, err)
		}
	}
	built := &Built{DNS: dns.Spec.ClusterIP, States: map[string]string{}}
	for _, svc := range plan.Services {
		created, err := core.Services(ns).Create(ctx, svc, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("copy Service %s: %w", svc.Name, err)
		}
		built.Services = append(built.Services, created)
	}
	cm, pod := dnsObjects(ns, Zone(built.Services, prodNS, domain))
	if _, err := core.ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("configure the Lab DNS: %w", err)
	}
	if _, err := core.Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("start the Lab DNS: %w", err)
	}
	if _, err := p.waitFor(ctx, ns, pod.Name, ready); err != nil {
		return nil, fmt.Errorf("the Lab DNS did not become ready: %w", err)
	}
	for _, pod := range plan.Pods {
		pod = pod.DeepCopy()
		pod.Spec.DNSConfig = DNSConfig(built.DNS, prodNS, domain)
		if _, err := core.Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			return nil, fmt.Errorf("start the copy of %s: %w", pod.Labels[CopyOf], err)
		}
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		settled := true
		for _, want := range plan.Pods {
			got, err := core.Pods(ns).Get(ctx, want.Name, metav1.GetOptions{})
			state := "not found"
			if err == nil {
				state = podState(got)
			}
			built.States[want.Labels[CopyOf]] = state
			settled = settled && state != "starting"
		}
		if settled || time.Now().After(deadline) {
			return built, nil
		}
		select {
		case <-ctx.Done():
			return built, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func ready(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// podState is "ready", "starting", or why the pod is failing.
func podState(pod *corev1.Pod) string {
	if ready(pod) {
		return "ready"
	}
	for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		switch {
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "ContainerCreating" && cs.State.Waiting.Reason != "PodInitializing":
			return cs.Name + " " + cs.State.Waiting.Reason
		case cs.RestartCount > 0 && cs.LastTerminationState.Terminated != nil:
			return fmt.Sprintf("%s restarted after %s (exit %d)", cs.Name, cs.LastTerminationState.Terminated.Reason, cs.LastTerminationState.Terminated.ExitCode)
		}
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return "unschedulable: " + c.Message
		}
	}
	return "starting"
}

// DNSLog reads what the world's pods asked the Lab DNS.
func (p *Prover) DNSLog(ctx context.Context, ns string) (string, error) {
	raw, err := p.Client.CoreV1().Pods(ns).GetLogs(dnsName, &corev1.PodLogOptions{LimitBytes: ptr.To[int64](1 << 20)}).DoRaw(ctx)
	return string(raw), err
}

// Clean removes everything the Lab created in a namespace: probes, worlds
// and their DNS, but not the seal.
func (p *Prover) Clean(ctx context.Context, ns string) error {
	core := p.Client.CoreV1()
	sel := metav1.ListOptions{LabelSelector: Label}
	var errs []string
	if err := core.Pods(ns).DeleteCollection(ctx, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)}, sel); err != nil {
		errs = append(errs, err.Error())
	}
	if err := core.ConfigMaps(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, sel); err != nil {
		errs = append(errs, err.Error())
	}
	svcs, err := core.Services(ns).List(ctx, sel)
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		for _, s := range svcs.Items {
			if err := core.Services(ns).Delete(ctx, s.Name, metav1.DeleteOptions{}); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("clean %s: %s", ns, strings.Join(errs, "; "))
	}
	return nil
}

// WorldProbes are the controls a running world adds to the matrix: each
// copied Service by its production name, which must reach the Lab's copy,
// and the API server by name, which the Lab DNS must not answer.
func WorldProbes(services []*corev1.Service, prodNS, domain string) []Probe {
	var out []Probe
	sorted := append([]*corev1.Service(nil), services...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, s := range sorted {
		for _, port := range s.Spec.Ports {
			if port.Protocol == "" || port.Protocol == corev1.ProtocolTCP {
				out = append(out, Probe{Name: "copy of " + s.Name + " by its production name",
					Address: hostPort(s.Name+"."+prodNS+".svc."+domain, int(port.Port)), Allowed: true})
				break
			}
		}
	}
	return append(out, Probe{Name: "API server by name", Address: hostPort("kubernetes.default.svc."+domain, 443)})
}
