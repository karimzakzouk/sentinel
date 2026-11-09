package lab

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

func experiment(ns, labNS, purpose string) *v1alpha1.Experiment {
	return &v1alpha1.Experiment{ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: ns},
		Spec: v1alpha1.ExperimentSpec{IncidentID: "inc", Purpose: purpose, LabNamespace: labNS}}
}

// The Lab runs only in its own namespaces, and only where NetworkPolicy
// enforcement is proven; it never seals or starts anything otherwise.
func TestTheLabRefusesBeforeItStartsAnything(t *testing.T) {
	cases := map[string]struct {
		exp      *v1alpha1.Experiment
		enforced bool
		why      string
	}{
		"outside the pool":             {experiment("shop", "shop", "prove"), true, "not one of the Lab's namespaces"},
		"in another namespace":         {experiment("sentinel-lab-1", "sentinel-lab-0", "prove"), true, "must live in the Lab namespace"},
		"where policy is not enforced": {experiment("sentinel-lab-0", "sentinel-lab-0", "prove"), false, "the Lab stays off"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := cluster()
			l := &Lab{Prover: &Prover{Client: client}, Pool: []string{"sentinel-lab-0", "sentinel-lab-1"},
				Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Enforced: func(context.Context, string) (bool, string) {
					return tc.enforced, "the probe's deny policy did not block traffic"
				}}
			phase, msg := l.Handle(ctx, tc.exp)
			if phase != PhaseFailed || !strings.Contains(msg, tc.why) {
				t.Fatalf("%s: %s; want Failed mentioning %q", phase, msg, tc.why)
			}
			if nps, _ := client.NetworkingV1().NetworkPolicies("sentinel-lab-0").List(ctx, metav1.ListOptions{}); len(nps.Items) != 0 {
				t.Fatal("the Lab sealed a namespace it refused to use")
			}
			if pods, _ := client.CoreV1().Pods("sentinel-lab-0").List(ctx, metav1.ListOptions{}); len(pods.Items) != 0 {
				t.Fatal("the Lab started a pod it refused to run")
			}
		})
	}
}

func TestTheSealVerdictSaysWhereItLeaks(t *testing.T) {
	r := func(name string, allowed, outside, reached bool) Result {
		return Result{Probe: Probe{Name: name, Allowed: allowed, Outside: outside}, Reached: reached}
	}
	control := r("echo service", true, false, true)
	cases := map[string]struct {
		results []Result
		phase   v1alpha1.ExperimentPhase
		says    string
	}{
		"sealed":             {[]Result{control, r("cluster DNS", false, false, false), r("cloud metadata", false, true, false)}, PhasePassed, "sealed: 2 forbidden destinations refused"},
		"leaks inside":       {[]Result{control, r("cluster DNS", false, false, true), r("cloud metadata", false, true, true)}, PhaseFailed, "the seal leaks to cluster DNS, and outside the cluster to cloud metadata"},
		"leaks only outside": {[]Result{control, r("cluster DNS", false, false, false), r("cloud metadata", false, true, true)}, PhaseFailed, "the seal leaks outside the cluster to cloud metadata: the network plugin lets pods reach them"},
		"a silent control":   {[]Result{r("echo service", true, false, false), r("cluster DNS", false, false, false)}, PhaseNotSure, "the positive controls did not answer (echo service)"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			phase, msg, sealed := sealVerdict(tc.results)
			if phase != tc.phase || !strings.Contains(msg, tc.says) || sealed != (tc.phase == PhasePassed) {
				t.Fatalf("%s, %q, sealed=%v; want %s saying %q", phase, msg, sealed, tc.phase, tc.says)
			}
		})
	}
}
