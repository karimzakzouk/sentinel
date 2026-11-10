package lab

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

func TestAnAllowReadsAsItIsWritten(t *testing.T) {
	for _, s := range []string{"tcp/10.96.0.10:53", "udp/10.96.0.10:53", "tcp/[fd00::a]:8080"} {
		a, err := ParseAllow(s)
		if err != nil || a.String() != s {
			t.Fatalf("%s read as %v (%v)", s, a, err)
		}
	}
	if a, err := ParseAllow("tcp/[::ffff:10.0.0.1]:80"); err != nil || a.String() != "tcp/10.0.0.1:80" {
		t.Fatalf("a mapped address read as %v (%v); want the IPv4 address", a, err)
	}
	for _, s := range []string{"10.0.0.1:80", "sctp/10.0.0.1:80", "tcp/10.0.0.1", "tcp/10.0.0.1:0", "tcp/example.com:80"} {
		if _, err := ParseAllow(s); err == nil {
			t.Errorf("%q was accepted", s)
		}
	}
}

// The firewall runs first, before the seal gate and the pod's own init
// containers, as root with NET_ADMIN alone and nothing to write; without a
// seal image the Lab leaves pods to NetworkPolicy.
func TestTheFirewallSealsAPodBeforeAnythingElseRuns(t *testing.T) {
	pod := labPod("sentinel-lab-0", "copy", nil, []string{"sh"})
	pod.Spec.InitContainers = []corev1.Container{{Name: "migrate"}}
	allow := []Allow{{Proto: "udp", Addr: netip.MustParseAddrPort("10.96.7.7:53")}}
	if got := (&Prover{}).seal(gated(pod, SealGate("10.96.7.7", "10.96.0.1:443")), allow); got.Spec.InitContainers[0].Name != GateName {
		t.Fatalf("without a seal image the pod changed: %v", got.Spec.InitContainers)
	}
	got := (&Prover{SealImage: "sentinel:2"}).seal(gated(pod, SealGate("10.96.7.7", "10.96.0.1:443")), allow)
	var names []string
	for _, c := range got.Spec.InitContainers {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{FirewallName, GateName, "migrate"}) {
		t.Fatalf("init containers = %v", names)
	}
	fw := got.Spec.InitContainers[0]
	sc := fw.SecurityContext
	switch {
	case fw.Image != "sentinel:2" || !slices.Equal(fw.Command, FirewallCommand) || !slices.Equal(fw.Args, []string{"lab-seal", "--allow=udp/10.96.7.7:53"}):
		t.Fatalf("firewall = %s %v %v", fw.Image, fw.Command, fw.Args)
	case !slices.Equal(sc.Capabilities.Add, []corev1.Capability{"NET_ADMIN"}) || !slices.Contains(sc.Capabilities.Drop, "ALL"):
		t.Fatalf("capabilities = %+v", sc.Capabilities)
	case ptr.Deref(sc.AllowPrivilegeEscalation, true) || !ptr.Deref(sc.ReadOnlyRootFilesystem, false) || ptr.Deref(sc.Privileged, false):
		t.Fatalf("security context = %+v", sc)
	case len(fw.VolumeMounts) > 0:
		t.Fatalf("the firewall mounts %v", fw.VolumeMounts)
	}
	for _, c := range got.Spec.InitContainers[1:] {
		if c.SecurityContext != nil && c.SecurityContext.Capabilities != nil && len(c.SecurityContext.Capabilities.Add) > 0 {
			t.Fatalf("%s gained %v", c.Name, c.SecurityContext.Capabilities.Add)
		}
	}
}

// A world's pods may reach the Lab DNS and the world's own Services, at
// every address and port they serve, and nothing else.
func TestAWorldMayReachOnlyItsDNSAndServices(t *testing.T) {
	svcs := []*corev1.Service{
		{Spec: corev1.ServiceSpec{ClusterIP: "10.96.3.3", ClusterIPs: []string{"10.96.3.3", "fd00::3"},
			Ports: []corev1.ServicePort{{Port: 7070}, {Port: 9090, Protocol: corev1.ProtocolUDP}, {Port: 3868, Protocol: corev1.ProtocolSCTP}}}},
		{Spec: corev1.ServiceSpec{ClusterIP: "10.96.4.4", Ports: []corev1.ServicePort{{Port: 6379, Protocol: corev1.ProtocolTCP}}}},
		{Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Ports: []corev1.ServicePort{{Port: 80}}}},
	}
	var got []string
	for _, a := range append(dnsAllow("10.96.9.9"), serviceAllow(svcs)...) {
		got = append(got, a.String())
	}
	want := []string{"udp/10.96.9.9:53", "tcp/10.96.9.9:53", "tcp/10.96.3.3:7070", "udp/10.96.3.3:9090", "tcp/[fd00::3]:7070", "udp/[fd00::3]:9090", "tcp/10.96.4.4:6379"}
	if !slices.Equal(got, want) {
		t.Fatalf("allowed %v\nwant    %v", got, want)
	}
	if got := podAllow("10.244.1.5", 8080, 8080, 9090, 0); len(got) != 2 || got[1].String() != "tcp/10.244.1.5:9090" {
		t.Fatalf("a copy's replay may reach %v", got)
	}
}

// Under auto, the Lab seals with NetworkPolicy where the cluster enforces
// it and has each pod seal itself elsewhere; policy never runs unproven,
// and firewall never waits on a probe it does not need.
func TestTheLabChoosesItsSeal(t *testing.T) {
	cases := map[string]struct {
		seal, image string
		enforced    bool
		firewall    bool
		off         string
		probed      bool
	}{
		"auto, enforced":                  {SealAuto, "sentinel:2", true, false, "", true},
		"auto, not enforced":              {SealAuto, "sentinel:2", false, true, "", true},
		"auto, not enforced and no image": {"", "", false, false, "no firewall image is configured", true},
		"policy, enforced":                {SealPolicy, "sentinel:2", true, false, "", true},
		"policy, not enforced":            {SealPolicy, "sentinel:2", false, false, "NetworkPolicy enforcement is not proven here", true},
		"firewall":                        {SealFirewall, "sentinel:2", true, true, "", false},
		"firewall without an image":       {SealFirewall, "", false, false, "no image configured", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			probed := false
			l := &Lab{Prover: &Prover{SealImage: "stale"}, Seal: tc.seal, SealImage: tc.image, Enforced: func(context.Context, string) (bool, string) {
				probed = true
				return tc.enforced, "the probe's deny policy did not block traffic"
			}}
			firewall, off := l.sealMode(ctx, "sentinel-lab-0")
			if firewall != tc.firewall || !strings.Contains(off, tc.off) || (tc.off == "") != (off == "") || probed != tc.probed {
				t.Fatalf("firewall=%v off=%q probed=%v; want %v %q %v", firewall, off, probed, tc.firewall, tc.off, tc.probed)
			}
			if p := l.prover(firewall); firewall && p.SealImage != tc.image || !firewall && p.SealImage != "" {
				t.Fatalf("the experiment's prover seals with %q", p.SealImage)
			}
		})
	}
}

// Where the firewall stands in for policy, a refusal is remembered for a
// while, so each experiment does not wait on the probe again; under policy
// alone the Lab asks again at once.
func TestAnUnenforcedClusterIsProbedOnceInAWhile(t *testing.T) {
	for seal, want := range map[string]int{SealAuto: 1, SealPolicy: 3} {
		probes := 0
		l := &Lab{Seal: seal, SealImage: "sentinel:2", Enforced: func(context.Context, string) (bool, string) {
			probes++
			return false, "not enforced"
		}}
		for range 3 {
			l.sealMode(ctx, "sentinel-lab-0")
		}
		if probes != want {
			t.Errorf("%s: %d probes in three experiments, want %d", seal, probes, want)
		}
	}
}

// fakeProofs makes a fake cluster's echo control ready and its prober
// finish: every control answers, and the forbidden destinations answer
// too unless the prober sealed itself, as on a plugin that leaks. It
// records, for each prober, whether it carried the firewall.
func fakeProofs(client interface {
	PrependReactor(verb, resource string, reaction k8stesting.ReactionFunc)
	Tracker() k8stesting.ObjectTracker
}) *[]bool {
	var firewalled []bool
	pods := corev1.SchemeGroupVersion.WithResource("pods")
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		pod := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		switch pod.Name {
		case "lab-echo":
			pod.Status.PodIP = "10.244.2.9"
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		case "lab-prober":
			fw := len(pod.Spec.InitContainers) > 0 && pod.Spec.InitContainers[0].Name == FirewallName
			firewalled = append(firewalled, fw)
			word := map[bool]string{true: "reached", false: "refused"}
			var lines []string
			for _, line := range strings.Split(pod.Spec.Containers[0].Command[2], "\n") {
				f := strings.Fields(line)
				if len(f) < 5 || f[0] != "probe" || f[1] != "1" {
					continue
				}
				allowed := f[4] == "10"
				lines = append(lines, "1 "+word[allowed || !fw]+" "+f[2])
				if !allowed {
					lines = append(lines, "2 "+word[!fw]+" "+f[2])
				}
			}
			pod.Status.Phase = corev1.PodSucceeded
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "probe",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: strings.Join(lines, "\n")}}}}
		}
		return false, nil, nil
	})
	client.PrependReactor("create", "services", func(a k8stesting.Action) (bool, runtime.Object, error) {
		a.(k8stesting.CreateAction).GetObject().(*corev1.Service).Spec.ClusterIP = "10.96.9.9"
		return false, nil, nil
	})
	client.PrependReactor("delete-collection", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		list, err := client.Tracker().List(pods, corev1.SchemeGroupVersion.WithKind("Pod"), a.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		for _, p := range list.(*corev1.PodList).Items {
			_ = client.Tracker().Delete(pods, p.Namespace, p.Name)
		}
		return true, nil, nil
	})
	return &firewalled
}

// Under auto, where the cluster enforces policy but the policy seal still
// leaks, the pods seal themselves instead, and the Lab remembers it: the
// next experiment seals by firewall at once.
func TestAutoSealsByFirewallWherePolicyLeaks(t *testing.T) {
	client := cluster()
	firewalled := fakeProofs(client)
	probes := 0
	l := &Lab{Prover: &Prover{Client: client, Twins: []string{"shop"}, Timeout: time.Second}, Pool: []string{"sentinel-lab-0"},
		Seal: SealAuto, SealImage: "sentinel:2", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Enforced: func(context.Context, string) (bool, string) {
			probes++
			return true, ""
		}}
	v := l.Handle(ctx, experiment("sentinel-lab-0", "sentinel-lab-0", "prove"))
	if v.Phase != PhasePassed || !strings.HasPrefix(v.Message, "sealed by each pod's own firewall") {
		t.Fatalf("%s: %s; want the firewall's seal to pass", v.Phase, v.Message)
	}
	if !slices.Equal(*firewalled, []bool{false, true}) {
		t.Fatalf("probers carried the firewall %v; want policy first, then the firewall", *firewalled)
	}
	if v := l.Handle(ctx, experiment("sentinel-lab-0", "sentinel-lab-0", "prove")); v.Phase != PhasePassed {
		t.Fatalf("the second experiment: %s: %s", v.Phase, v.Message)
	}
	if !slices.Equal(*firewalled, []bool{false, true, true}) || probes != 1 {
		t.Fatalf("probers carried the firewall %v after %d policy probes; want the firewall at once", *firewalled, probes)
	}
	l.Seal = SealPolicy
	l.proven = nil
	if v := l.Handle(ctx, experiment("sentinel-lab-0", "sentinel-lab-0", "prove")); v.Phase != PhaseFailed || !strings.Contains(v.Message, "the seal leaks") {
		t.Fatalf("under policy alone: %s: %s; want the leak to fail it", v.Phase, v.Message)
	}
}

// A prove Experiment where the firewall seals starts every pod with it,
// the prober allowed only its two controls; it never asks the cluster
// whether NetworkPolicy is enforced.
func TestAFirewallProofSealsEveryPod(t *testing.T) {
	client := cluster()
	var created []*corev1.Pod
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		pod := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		created = append(created, pod.DeepCopy())
		if pod.Name == "lab-echo" {
			pod.Status.PodIP = "10.244.2.9"
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		return false, nil, nil
	})
	client.PrependReactor("create", "services", func(a k8stesting.Action) (bool, runtime.Object, error) {
		a.(k8stesting.CreateAction).GetObject().(*corev1.Service).Spec.ClusterIP = "10.96.9.9"
		return false, nil, nil
	})
	l := &Lab{Prover: &Prover{Client: client, Twins: []string{"shop"}, Timeout: 50 * time.Millisecond}, Pool: []string{"sentinel-lab-0"},
		Seal: SealFirewall, SealImage: "sentinel:2", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Enforced: func(context.Context, string) (bool, string) {
			t.Fatal("the firewall seal asked whether policy is enforced")
			return false, ""
		}}
	v := l.Handle(ctx, experiment("sentinel-lab-0", "sentinel-lab-0", "prove"))
	if v.Phase != PhaseNotSure || !strings.Contains(v.Message, "the prober did not finish") {
		t.Fatalf("%s: %s; want NOT SURE, as the fake prober never finishes", v.Phase, v.Message)
	}
	if len(created) != 2 {
		t.Fatalf("started %d pods, want the echo control and the prober", len(created))
	}
	for _, pod := range created {
		if fw := pod.Spec.InitContainers; len(fw) == 0 || fw[0].Name != FirewallName || fw[0].Image != "sentinel:2" {
			t.Fatalf("%s started without the firewall first: %v", pod.Name, fw)
		}
	}
	if args := created[0].Spec.InitContainers[0].Args; !slices.Equal(args, []string{"lab-seal"}) {
		t.Fatalf("the echo control may connect to %v; want nothing", args)
	}
	if args := created[1].Spec.InitContainers[0].Args; !slices.Equal(args, []string{"lab-seal", "--allow=tcp/10.96.9.9:8080", "--allow=tcp/10.244.2.9:8080"}) {
		t.Fatalf("the prober may connect to %v; want only its controls", args)
	}
}
