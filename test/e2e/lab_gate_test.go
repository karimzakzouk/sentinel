//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/internal/capability"
	"github.com/karimzakzouk/sentinel/internal/lab"
	"github.com/karimzakzouk/sentinel/internal/model"
)

// The Lab's gate, run on demand by the Lab gate workflow: races whose right
// answers are known, LAB_GATE_RACES times each. A fix must pass; a revision
// that crashes differently, a deletion-style candidate that starts but does
// nothing, and a flaky revision raced against itself must never pass. The
// tally goes to LAB_GATE_REPORT, as Markdown, when that is set.
func TestTheLabsVerdictsHoldTheirErrorRates(t *testing.T) {
	races, _ := strconv.Atoi(os.Getenv("LAB_GATE_RACES"))
	if races <= 0 {
		t.Skip("set LAB_GATE_RACES to run the Lab gate's repetitions")
	}
	ctx := context.Background()
	client := clientAs(t, labUser)
	ref := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: smokeNS, Name: "front"}
	world, err := lab.ReadWorld(ctx, client, smokeNS, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	front := world.Workloads[ref].Template
	variant := func(command ...string) (*corev1.Pod, *lab.Plan) {
		w := world
		w.Workloads = maps.Clone(world.Workloads)
		wl := w.Workloads[ref]
		wl.Template = *front.DeepCopy()
		if command != nil {
			wl.Template.Spec.Containers[0].Command = command
		}
		w.Workloads[ref] = wl
		plan, err := lab.Synthesize(w, ref, labNS)
		if err != nil {
			t.Fatal(err)
		}
		pod, deps := plan.Split(ref)
		return pod, deps
	}
	fix, deps := variant()
	failing, _ := variant("sh", "-c", "echo starting; exit 3")
	stillBroken, _ := variant("sh", "-c", "echo still broken; exit 4")
	flaky, _ := variant("sh", "-c",
		`n=$(od -An -N1 -tu1 /dev/urandom); if [ $((n % 2)) -eq 0 ]; then echo crash; exit 3; fi; exec /agnhost netexec --http-port=8080`)

	p := &lab.Prover{Client: client, Twins: []string{smokeNS}, Timeout: 3 * time.Minute}
	if got := (capability.NetworkPolicyProbe{Client: clientAs(t, ""), Namespace: labNS, Settle: 30 * time.Second}).Run(ctx); got.Status != capability.Available {
		t.Logf("NetworkPolicy is not enforced here (%s): these races test the Lab's mechanics, not its seal", got.Detail)
		p.Forbidden = unroutable
	}
	if err := p.Clean(ctx, labNS); err != nil {
		t.Fatal(err)
	}
	if err := p.Seal(ctx, labNS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Clean(ctx, labNS)
		_ = client.NetworkingV1().NetworkPolicies(labNS).Delete(ctx, "lab-internal", metav1.DeleteOptions{})
	})
	built, err := p.Build(ctx, labNS, smokeNS, "cluster.local", deps)
	if err != nil {
		t.Fatal(err)
	}
	dns := lab.DNSConfig(built.DNS, smokeNS, "cluster.local")

	type tally struct {
		passed, failed, notSure int
		blocks                  []int
	}
	kinds := []struct {
		name                string
		baseline, candidate *corev1.Pod
		mayPass             bool
	}{
		{"a fix", failing, fix, true},
		{"a revision that still crashes", failing, stillBroken, false},
		{"a deletion that starts but does nothing", failing, lab.DeletionControl(failing), false},
		{"a flaky revision against itself", flaky, flaky, false},
	}
	tallies := map[string]*tally{}
	for i := range races {
		for _, k := range kinds {
			race := lab.NewRace(0.05, []string{"candidate"}, []string{"deletion control"})
			variants := []lab.Variant{{Name: "candidate", Slug: "cand", Pod: k.candidate},
				{Name: "deletion control", Slug: "del", Pod: lab.DeletionControl(k.baseline)}}
			blocks, err := p.RunRace(ctx, labNS, built, dns, k.baseline, variants, race, 60)
			if err != nil {
				t.Fatalf("%s, race %d: %v", k.name, i+1, err)
			}
			phase, msg, _ := race.Outcome(false)
			t.Logf("%s, race %d: %s after %d blocks: %s", k.name, i+1, phase, blocks, msg)
			tl := tallies[k.name]
			if tl == nil {
				tl = &tally{}
				tallies[k.name] = tl
			}
			tl.blocks = append(tl.blocks, blocks)
			switch phase {
			case lab.PhasePassed:
				tl.passed++
			case lab.PhaseFailed:
				tl.failed++
			default:
				tl.notSure++
			}
		}
	}

	var report strings.Builder
	fmt.Fprintf(&report, "## Lab gate\n\n%d races of each kind, at α = 5%%.\n\n", races)
	report.WriteString("| Candidate | Must pass | Passed | Failed | Not sure | Median blocks |\n|---|---|---|---|---|---|\n")
	for _, k := range kinds {
		tl := tallies[k.name]
		slices.Sort(tl.blocks)
		fmt.Fprintf(&report, "| %s | %v | %d | %d | %d | %d |\n", k.name, k.mayPass, tl.passed, tl.failed, tl.notSure, tl.blocks[len(tl.blocks)/2])
		switch {
		case !k.mayPass && tl.passed > 0:
			t.Errorf("%s passed %d of %d races", k.name, tl.passed, races)
		case k.mayPass && tl.passed < races*9/10:
			t.Errorf("%s passed only %d of %d races", k.name, tl.passed, races)
		}
	}
	t.Log("\n" + report.String())
	if path := os.Getenv("LAB_GATE_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(report.String()), 0o600); err != nil {
			t.Error(err)
		}
	}
}

// Online Boutique's frontend copied into the Lab with everything it calls,
// which is slice 2's proof: every copy starts, each answers by its
// production name, and none of production is reachable. It needs Online
// Boutique deployed (make boutique-up), and skips otherwise.
func TestTheLabCopiesOnlineBoutiquesFrontend(t *testing.T) {
	ctx := context.Background()
	client := clientAs(t, labUser)
	if _, err := client.AppsV1().Deployments("boutique").List(ctx, metav1.ListOptions{}); err != nil {
		t.Skipf("the Lab may not read Online Boutique: %v", err)
	}
	ref := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "boutique", Name: "frontend"}
	world, err := lab.ReadWorld(ctx, client, "boutique", ref, "")
	if err != nil {
		t.Skipf("Online Boutique is not deployed (make boutique-up): %v", err)
	}
	plan, err := lab.Synthesize(world, ref, labNS)
	if err != nil {
		t.Fatal(err)
	}
	var copied []string
	for _, w := range plan.Workloads {
		copied = append(copied, w.Name)
	}
	t.Logf("copied %v", copied)
	for _, want := range []string{"frontend", "productcatalogservice", "cartservice", "redis-cart", "checkoutservice", "paymentservice", "emailservice"} {
		if !slices.Contains(copied, want) {
			t.Errorf("the frontend's world lacks %s", want)
		}
	}
	if slices.Contains(copied, "loadgenerator") {
		t.Error("the world holds the load generator, which calls the frontend but is not called by it")
	}
	p := &lab.Prover{Client: client, Twins: []string{"boutique"}, Timeout: 5 * time.Minute}
	got := capability.NetworkPolicyProbe{Client: clientAs(t, ""), Namespace: labNS, Settle: 30 * time.Second}.Run(ctx)
	enforced := got.Status == capability.Available
	if !enforced {
		t.Logf("NetworkPolicy is not enforced here (%s): this tests the copy, not the seal", got.Detail)
		p.Forbidden = unroutable
	}
	if err := p.Clean(ctx, labNS); err != nil {
		t.Fatal(err)
	}
	if err := p.Seal(ctx, labNS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Clean(ctx, labNS)
		_ = client.NetworkingV1().NetworkPolicies(labNS).Delete(ctx, "lab-internal", metav1.DeleteOptions{})
	})
	built, err := p.Build(ctx, labNS, "boutique", "cluster.local", plan)
	if err != nil {
		t.Fatal(err)
	}
	for w, state := range built.States {
		t.Logf("%-40s %s", w, state)
		if state != "ready" {
			t.Errorf("the copy of %s is %s", w, state)
		}
	}
	probes := lab.WorldProbes(built.Services, "boutique", "cluster.local")
	results, err := p.Prove(ctx, labNS, &lab.Inside{Probes: probes, DNS: lab.DNSConfig(built.DNS, "boutique", "cluster.local")})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		switch {
		case r.Allowed && !r.Reached:
			t.Errorf("%s did not answer", r.Name)
		case !r.Holds() && enforced && (!r.Outside || strictEgress()):
			t.Errorf("with the frontend's world running, the seal leaks to %s", r.Name)
		case !r.Holds():
			t.Logf("reached %s, which the seal does not stop here", r.Name)
		}
	}
	for _, f := range plan.Fidelity {
		t.Logf("fidelity: %s", f)
	}
	if log, err := p.DNSLog(ctx, labNS); err == nil {
		t.Logf("names the world asked for that the Lab does not provide: %v",
			lab.Unresolved(log, lab.DNSConfig(built.DNS, "boutique", "cluster.local").Searches, []string{"kubernetes.default.svc.cluster.local"}))
	}
}
