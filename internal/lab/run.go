package lab

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/model"
)

// ExperimentsResource is the Experiment custom resource.
var ExperimentsResource = v1alpha1.GroupVersion.WithResource("experiments")

// Phases of an Experiment.
const (
	PhasePending v1alpha1.ExperimentPhase = "Pending"
	PhaseSealing v1alpha1.ExperimentPhase = "Sealing"
	PhaseRunning v1alpha1.ExperimentPhase = "Running"
	PhasePassed  v1alpha1.ExperimentPhase = "Passed"
	PhaseFailed  v1alpha1.ExperimentPhase = "Failed"
	PhaseNotSure v1alpha1.ExperimentPhase = "NotSure"
	PhaseAborted v1alpha1.ExperimentPhase = "Aborted"
)

func done(p v1alpha1.ExperimentPhase) bool {
	return p == PhasePassed || p == PhaseFailed || p == PhaseNotSure || p == PhaseAborted
}

// Lab runs Experiments in its pool of namespaces, one at a time.
type Lab struct {
	Prover  *Prover
	Dynamic dynamic.Interface
	// Pool lists the Lab namespaces created at install.
	Pool []string
	// Domain is the cluster's DNS domain.
	Domain string
	// Enforced proves that a namespace's NetworkPolicy is enforced, with
	// live traffic. Without it the Lab runs only where it may seal its pods
	// itself.
	Enforced func(ctx context.Context, ns string) (bool, string)
	// Seal is how the Lab seals a namespace: SealPolicy, SealFirewall or
	// SealAuto, the default.
	Seal string
	// SealImage is the image of the firewall that seals each pod, for
	// SealFirewall and SealAuto: Sentinel's own.
	SealImage string
	Log       *slog.Logger
	// OnReady, when set, is called once the Lab sees its pool's
	// Experiments.
	OnReady func()

	mu      sync.Mutex
	proven  map[string]proof
	handled map[string]bool
}

// How the Lab seals its namespaces.
const (
	// SealPolicy seals with NetworkPolicy, which the cluster must be
	// proven to enforce.
	SealPolicy = "policy"
	// SealFirewall has every Lab pod seal itself with a firewall it sets
	// up before anything else in it runs.
	SealFirewall = "firewall"
	// SealAuto seals with policy where the cluster enforces it, and with
	// the firewall elsewhere.
	SealAuto = "auto"
)

// provenFor is how long a namespace's proof of enforcement, or of its
// absence, is trusted.
const provenFor = 10 * time.Minute

type proof struct {
	at       time.Time
	enforced bool
	why      string
}

// Verdict is how an experiment ended, why, how its world differed from
// production, and how many blocks it ran.
type Verdict struct {
	Phase    v1alpha1.ExperimentPhase
	Message  string
	Fidelity []string
	Runs     int
	// judged says the verdict is the race's, not a refusal to run.
	judged bool
}

func failed(format string, args ...any) Verdict {
	return Verdict{Phase: PhaseFailed, Message: fmt.Sprintf(format, args...)}
}

// Handle runs one Experiment. The Lab proves its seal before anything runs
// and again with the world running, and removes everything it started.
// Only a prove Experiment fails for a seal that does not hold; for the
// others, Failed is the race's verdict on the fix, and an experiment the
// Lab could not run is NOT SURE.
func (l *Lab) Handle(ctx context.Context, exp *v1alpha1.Experiment) Verdict {
	v := l.handle(ctx, exp)
	if exp.Spec.Purpose != "prove" && v.Phase == PhaseFailed && !v.judged {
		v.Phase = PhaseNotSure
	}
	return v
}

func (l *Lab) handle(ctx context.Context, exp *v1alpha1.Experiment) Verdict {
	ns := exp.Spec.LabNamespace
	switch {
	case !slices.Contains(l.Pool, ns):
		return failed("%s is not one of the Lab's namespaces", ns)
	case exp.Namespace != ns:
		return failed("an Experiment must live in the Lab namespace it uses")
	}
	var (
		plan   *Plan
		world  World
		ref    model.ObjectRef
		target = exp.Spec.Workload
	)
	switch exp.Spec.Purpose {
	case "prove":
	case "reproduce", "verify-fix":
		switch {
		case target == nil:
			return failed("the experiment names no workload")
		case !slices.Contains(l.Prover.Twins, target.Namespace):
			return failed("the Lab copies only from the namespaces Sentinel watches, not %s", target.Namespace)
		}
		ref = model.ObjectRef{Group: "apps", Kind: target.Kind, Namespace: target.Namespace, Name: target.Name}
		var err error
		if world, err = ReadWorld(ctx, l.Prover.Client, target.Namespace, ref, exp.Spec.Revision); err != nil {
			return failed("could not read %s: %v", ref, err)
		}
		if plan, err = Synthesize(world, ref, ns); err != nil {
			return failed("%v", err)
		}
	default:
		return Verdict{Phase: PhaseNotSure, Message: "the Lab cannot " + exp.Spec.Purpose + " yet"}
	}
	firewall, off := l.sealMode(ctx, ns)
	if off != "" {
		return failed("the Lab stays off: %s", off)
	}
	p := l.prover(firewall)
	cleanup := context.WithoutCancel(ctx)
	if err := p.Clean(ctx, ns); err != nil {
		return failed("could not clear %s: %v", ns, err)
	}
	defer func() {
		if err := p.Clean(cleanup, ns); err != nil {
			l.Log.Error("could not clear the Lab namespace", "namespace", ns, "error", err)
		}
	}()
	if err := p.Seal(ctx, ns); err != nil {
		return failed("could not seal %s: %v", ns, err)
	}
	defer func() {
		_ = p.Client.NetworkingV1().NetworkPolicies(ns).Delete(cleanup, "lab-internal", metav1.DeleteOptions{})
	}()
	results, err := p.Prove(ctx, ns, nil)
	if err != nil {
		return Verdict{Phase: PhaseNotSure, Message: fmt.Sprintf("could not prove the seal: %v", err)}
	}
	phase, msg, sealed := sealVerdict(results)
	if !sealed && !firewall && l.Seal != SealPolicy && l.SealImage != "" {
		// Policy is enforced here, but not well enough to seal: the pods
		// seal themselves instead, and do from now on.
		l.Log.Info("the policy seal leaks; the Lab's pods seal themselves", "namespace", ns, "why", msg)
		l.remember(ns, false, msg)
		firewall, p = true, l.prover(true)
		if results, err = p.Prove(ctx, ns, nil); err != nil {
			return Verdict{Phase: PhaseNotSure, Message: fmt.Sprintf("could not prove the seal: %v", err)}
		}
		phase, msg, sealed = sealVerdict(results)
	}
	if firewall {
		msg = firewallSays(msg, sealed)
	}
	if !sealed || plan == nil {
		return Verdict{Phase: phase, Message: msg}
	}
	domain := cmp.Or(l.Domain, "cluster.local")
	baseline, deps := plan.Split(ref)
	built, err := p.Build(ctx, ns, target.Namespace, domain, deps)
	if err != nil {
		return Verdict{Phase: PhaseNotSure, Message: fmt.Sprintf("could not start the world: %v", err), Fidelity: plan.Fidelity}
	}
	dns := DNSConfig(built.DNS, target.Namespace, domain)
	probes := WorldProbes(readyServices(built, deps), target.Namespace, domain)
	results, err = p.Prove(ctx, ns, &Inside{Probes: probes, DNS: dns, Allow: built.Allow})
	if err != nil {
		return Verdict{Phase: PhaseNotSure, Message: fmt.Sprintf("could not prove the seal with the world running: %v", err), Fidelity: plan.Fidelity}
	}
	if phase, msg, sealed := sealVerdict(results); !sealed {
		if firewall {
			msg = firewallSays(msg, sealed)
		}
		return Verdict{Phase: phase, Message: "with the world running, " + msg, Fidelity: plan.Fidelity}
	}
	fidelity := slices.Clone(plan.Fidelity)
	if firewall {
		fidelity = append(fidelity, "this cluster does not enforce NetworkPolicy, so every Lab pod sealed itself with Sentinel's firewall "+
			"before anything else in it ran: copies reach only the Lab, though the cluster can still reach them")
	}
	var variants []Variant
	var candidates, controls []string
	if exp.Spec.Purpose == "verify-fix" && exp.Spec.Candidate != "" {
		// A fix to the failing image's code, built by its source's
		// pipeline: the failing revision with that image instead.
		pod, err := ImageCandidate(baseline, exp.Spec.Candidate)
		if err != nil {
			return Verdict{Phase: PhaseNotSure, Message: "the Lab has no fix to try: " + err.Error(), Fidelity: fidelity}
		}
		fidelity = append(fidelity, "the candidate image "+exp.Spec.Candidate+" is not pinned to a digest")
		variants = []Variant{{Name: "image " + exp.Spec.Candidate, Slug: "img", Pod: pod},
			{Name: "deletion control", Slug: "del", Pod: DeletionControl(baseline)}}
		candidates, controls = []string{variants[0].Name}, []string{variants[1].Name}
	} else if exp.Spec.Purpose == "verify-fix" && len(exp.Spec.Overrides) > 0 {
		// Values a fix would set, learned from healthy use: the failing
		// revision with them in place.
		pod, err := ValuesCandidate(baseline, exp.Spec.Overrides)
		if err != nil {
			return Verdict{Phase: PhaseNotSure, Message: "the Lab has no fix to try: " + err.Error(), Fidelity: fidelity}
		}
		variants = []Variant{{Name: "the failing revision with " + Overridden(exp.Spec.Overrides), Slug: "val", Pod: pod},
			{Name: "deletion control", Slug: "del", Pod: DeletionControl(baseline)}}
		candidates, controls = []string{variants[0].Name}, []string{variants[1].Name}
	} else if exp.Spec.Purpose == "verify-fix" {
		pod, revision, notes, err := l.candidate(ctx, world, ref, exp.Spec.Revision, ns)
		if err != nil {
			return Verdict{Phase: PhaseNotSure, Message: "the Lab has no fix to try: " + err.Error(), Fidelity: fidelity}
		}
		for _, n := range notes {
			if !slices.Contains(fidelity, n) {
				fidelity = append(fidelity, n)
			}
		}
		variants = []Variant{{Name: "previous revision " + revision, Slug: "cand", Pod: pod}}
		// When the previous revision changed more than configuration, the
		// failing one with only its configuration reverted says which part
		// of the rollback fixes it.
		if cfg, ok := ConfigRevert(baseline, pod); ok && !sameContainers(cfg, pod) {
			variants = append(variants, Variant{Name: "failing revision with the configuration of " + revision, Slug: "cfg", Pod: cfg})
		}
		for _, v := range variants {
			candidates = append(candidates, v.Name)
		}
		variants = append(variants, Variant{Name: "deletion control", Slug: "del", Pod: DeletionControl(baseline)})
		controls = []string{"deletion control"}
	}
	race := NewRace(float64(cmp.Or(exp.Spec.AlphaPercent, 5))/100, candidates, controls)
	fidelity = append(fidelity, "every copy waits until the seal holds for it before it starts")
	traffic := Traffic{Requests: withPorts(exp.Spec.Replay, baseline), ServerErrors: len(plan.Empty) > 0}
	switch {
	case len(traffic.Requests) == 0:
		fidelity = append(fidelity, "no traffic is replayed: a copy works if it stays ready")
	case traffic.ServerErrors:
		fidelity = append(fidelity, fmt.Sprintf("each copy is sent %d reads production answered, and must answer every one; "+
			"with datastores empty, a server error is not held against it", len(traffic.Requests)))
	default:
		fidelity = append(fidelity, fmt.Sprintf("each copy is sent %d reads production answered, and must answer every one without a server error",
			len(traffic.Requests)))
	}
	blocks, err := p.RunRace(ctx, ns, built, dns, baseline, variants, traffic, race, int(cmp.Or(exp.Spec.MaxRuns, 200)))
	if log, lerr := p.DNSLog(ctx, ns); lerr == nil {
		var own []string
		for _, p := range probes {
			if host, _, err := net.SplitHostPort(p.Address); err == nil {
				own = append(own, host)
			}
		}
		tokens := secretTokens(world)
		for _, name := range Unresolved(log, dns.Searches, own) {
			if from := tokenIn(name, tokens); from != "" {
				fidelity = append(fidelity, fmt.Sprintf("the world looked up %s, a name taken from %s, which holds a placeholder in the Lab", name, from))
				continue
			}
			fidelity = append(fidelity, fmt.Sprintf("the world asked for %s, which the Lab does not provide", name))
		}
	} else {
		fidelity = append(fidelity, "the Lab DNS log could not be read, so names the world missed are unknown")
	}
	if err != nil {
		return Verdict{Phase: PhaseNotSure, Message: fmt.Sprintf("the runs stopped after %d blocks: %v", blocks, err), Fidelity: fidelity, Runs: blocks}
	}
	phase, msg, _ = race.Outcome(false)
	return Verdict{Phase: phase, Message: msg, Fidelity: fidelity, Runs: blocks, judged: true}
}

// candidate synthesizes the workload's revision before the failing one,
// copying any ConfigMap only it reads, and says which revision it is and
// how its copy differs from production.
func (l *Lab) candidate(ctx context.Context, world World, ref model.ObjectRef, revision, ns string) (*corev1.Pod, string, []string, error) {
	tpl, name, err := PreviousTemplate(ctx, l.Prover.Client, ref, revision)
	if err != nil {
		return nil, "", nil, err
	}
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	pod, err := copyPod(Workload{Ref: ref, Template: tpl, Replicas: 1}, world, ns, note)
	if err != nil {
		return nil, "", nil, err
	}
	core := l.Prover.Client.CoreV1()
	for _, cm := range configMapsOf(pod.Spec) {
		if _, err := core.ConfigMaps(ns).Get(ctx, cm.name, metav1.GetOptions{}); err == nil {
			continue
		}
		src, err := core.ConfigMaps(ref.Namespace).Get(ctx, cm.name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err) && cm.optional:
			continue
		case err != nil:
			return nil, "", nil, fmt.Errorf("revision %s needs ConfigMap %s: %w", name, cm.name, err)
		}
		cp := &corev1.ConfigMap{ObjectMeta: worldMeta(ns, src.Name, nil), Data: src.Data, BinaryData: src.BinaryData}
		if _, err := core.ConfigMaps(ns).Create(ctx, cp, metav1.CreateOptions{}); err != nil {
			return nil, "", nil, fmt.Errorf("copy ConfigMap %s: %w", cm.name, err)
		}
	}
	return pod, name, notes, nil
}

// secretTokens maps each placeholder the world's copies hold to the key of
// the Secret it stands in for.
func secretTokens(w World) map[string]string {
	out := map[string]string{}
	for _, wl := range w.Workloads {
		for _, c := range append(wl.Template.Spec.InitContainers, wl.Template.Spec.Containers...) {
			for _, e := range c.Env {
				if r := e.ValueFrom; r != nil && r.SecretKeyRef != nil {
					out[SecretPlaceholder(r.SecretKeyRef.Name, r.SecretKeyRef.Key)] = fmt.Sprintf("key %s of secret %s", r.SecretKeyRef.Key, r.SecretKeyRef.Name)
				}
			}
		}
	}
	return out
}

// tokenIn names the secret key whose placeholder a looked-up name holds.
func tokenIn(name string, tokens map[string]string) string {
	if !strings.Contains(name, placeholderPrefix) {
		return ""
	}
	for token, from := range tokens {
		if strings.Contains(name, token) {
			return from
		}
	}
	return ""
}

// readyServices are the world's Services whose every copy is ready, so a
// control through them must answer; a failing copy is the subject, not the
// seal.
func readyServices(built *Built, plan *Plan) []*corev1.Service {
	var out []*corev1.Service
	for _, svc := range built.Services {
		sel := labels.SelectorFromSet(svc.Spec.Selector)
		ok, any := true, false
		for _, pod := range plan.Pods {
			if sel.Matches(labels.Set(pod.Labels)) {
				any = true
				ok = ok && built.States[pod.Labels[CopyOf]] == "ready"
			}
		}
		if ok && any {
			out = append(out, svc)
		}
	}
	return out
}

// sealVerdict reads the matrix: the seal holds when every forbidden
// destination was refused in both passes and every control answered.
func sealVerdict(results []Result) (v1alpha1.ExperimentPhase, string, bool) {
	var inside, outside, silent []string
	steady, refused := false, 0
	for _, r := range results {
		switch {
		case r.Allowed && !r.Reached:
			silent = append(silent, r.Name)
		case r.Allowed:
		case !r.Reached && !r.Later:
			refused++
		case r.Outside:
			outside = append(outside, r.Name)
		default:
			inside = append(inside, r.Name)
			steady = steady || r.Later
		}
	}
	switch {
	case len(inside) > 0:
		msg := "the seal leaks to " + strings.Join(inside, ", ")
		if !steady {
			msg = "the seal leaks while a pod starts, to " + strings.Join(inside, ", ")
		}
		if len(outside) > 0 {
			msg += ", and outside the cluster to " + strings.Join(outside, ", ")
		}
		if !steady {
			msg += ": the network plugin applies policy to a new pod only after it starts, so a copy could reach them as it starts"
		}
		return PhaseFailed, msg, false
	case len(outside) > 0:
		return PhaseFailed, "the seal leaks outside the cluster to " + strings.Join(outside, ", ") +
			": the network plugin lets pods reach them despite the policy, so block them at the nodes before using the Lab here", false
	case len(silent) > 0:
		return PhaseNotSure, "the positive controls did not answer (" + strings.Join(silent, ", ") + "), so a refusal means nothing", false
	}
	return PhasePassed, fmt.Sprintf("sealed: %d forbidden destinations refused and the controls answered", refused), true
}

// firewallSays words a seal verdict for a firewall seal, whose leaks are
// the firewall's, not the network plugin's.
func firewallSays(msg string, sealed bool) string {
	if sealed {
		return strings.Replace(msg, "sealed:", "sealed by each pod's own firewall:", 1)
	}
	msg, _, _ = strings.Cut(msg, ": the network plugin")
	return "with each pod's own firewall, " + msg
}

// sealMode says whether this experiment's pods seal themselves with the
// firewall, or why the Lab may not run at all.
func (l *Lab) sealMode(ctx context.Context, ns string) (firewall bool, off string) {
	switch l.Seal {
	case SealFirewall:
		if l.SealImage == "" {
			return false, "the firewall seal has no image configured"
		}
		return true, ""
	case SealPolicy:
		if ok, why := l.enforced(ctx, ns); !ok {
			return false, "NetworkPolicy enforcement is not proven here (" + why + ")"
		}
		return false, ""
	}
	ok, why := l.enforced(ctx, ns)
	switch {
	case ok:
		return false, ""
	case l.SealImage == "":
		return false, "NetworkPolicy enforcement is not proven here (" + why + "), and no firewall image is configured"
	}
	return true, ""
}

// prover is the Lab's Prover for one experiment, sealing each pod with the
// firewall when firewall is set.
func (l *Lab) prover(firewall bool) *Prover {
	p := *l.Prover
	p.SealImage = ""
	if firewall {
		p.SealImage = l.SealImage
	}
	return &p
}

func (l *Lab) enforced(ctx context.Context, ns string) (bool, string) {
	if l.Enforced == nil {
		return false, "no enforcement check is configured"
	}
	l.mu.Lock()
	pr, ok := l.proven[ns]
	l.mu.Unlock()
	// A refusal is remembered only where the firewall stands in for policy,
	// so a policy seal tries again at once once enforcement is fixed.
	if ok && time.Since(pr.at) < provenFor && (pr.enforced || l.Seal != SealPolicy) {
		return pr.enforced, pr.why
	}
	enforced, why := l.Enforced(ctx, ns)
	l.remember(ns, enforced, why)
	return enforced, why
}

// remember holds what the Lab learned of a namespace's NetworkPolicy for
// provenFor.
func (l *Lab) remember(ns string, enforced bool, why string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.proven == nil {
		l.proven = map[string]proof{}
	}
	l.proven[ns] = proof{at: time.Now(), enforced: enforced, why: why}
}

// Run watches Experiments in the pool and runs each new one, until ctx ends.
func (l *Lab) Run(ctx context.Context) error {
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()
	stores := map[string]cache.Store{}
	var synced []cache.InformerSynced
	for _, ns := range l.Pool {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(l.Dynamic, 0, ns, nil)
		inf := f.ForResource(ExperimentsResource).Informer()
		enqueue := func(obj any) {
			if key, err := cache.MetaNamespaceKeyFunc(obj); err == nil {
				queue.Add(key)
			}
		}
		if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{AddFunc: enqueue, UpdateFunc: func(_, obj any) { enqueue(obj) }}); err != nil {
			return err
		}
		stores[ns] = inf.GetStore()
		synced = append(synced, inf.HasSynced)
		f.Start(ctx.Done())
	}
	wait, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if !cache.WaitForCacheSync(wait.Done(), synced...) {
		var stuck []string
		for i, ns := range l.Pool {
			if !synced[i]() {
				stuck = append(stuck, ns)
			}
		}
		return fmt.Errorf("experiments in %s did not sync within a minute: the Lab may lack permission there", strings.Join(stuck, ", "))
	}
	for _, ns := range l.Pool {
		if err := l.Prover.Clean(ctx, ns); err != nil {
			l.Log.Error("could not clear a Lab namespace", "namespace", ns, "error", err)
		}
		_ = l.Prover.Client.NetworkingV1().NetworkPolicies(ns).Delete(ctx, "lab-internal", metav1.DeleteOptions{})
	}
	if l.OnReady != nil {
		l.OnReady()
	}
	l.Log.Info("lab started", "pool", l.Pool)
	go func() {
		<-ctx.Done()
		queue.ShutDown()
	}()
	for {
		key, quit := queue.Get()
		if quit {
			return nil
		}
		if err := l.process(ctx, stores, key); err != nil {
			l.Log.Error("could not process an experiment", "experiment", key, "error", err)
			queue.AddRateLimited(key)
		} else {
			queue.Forget(key)
		}
		queue.Done(key)
	}
}

func (l *Lab) process(ctx context.Context, stores map[string]cache.Store, key string) error {
	ns, _, err := cache.SplitMetaNamespaceKey(key)
	if err != nil || stores[ns] == nil {
		return nil
	}
	obj, exists, err := stores[ns].GetByKey(key)
	if err != nil || !exists {
		return err
	}
	var exp v1alpha1.Experiment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(*unstructured.Unstructured).Object, &exp); err != nil {
		return err
	}
	l.mu.Lock()
	handled := l.handled[string(exp.UID)]
	l.mu.Unlock()
	switch {
	case done(exp.Status.Phase) || handled:
		return nil
	case exp.Status.Phase == PhaseSealing || exp.Status.Phase == PhaseRunning:
		return l.setStatus(ctx, &exp, PhaseAborted, "the Lab stopped while the experiment ran")
	}
	if err := l.setStatus(ctx, &exp, PhaseSealing, "sealing "+exp.Spec.LabNamespace); err != nil {
		return err
	}
	l.mu.Lock()
	if l.handled == nil {
		l.handled = map[string]bool{}
	}
	l.handled[string(exp.UID)] = true
	l.mu.Unlock()
	timeout := exp.Spec.Timeout.Duration
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	run, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	v := l.Handle(run, &exp)
	if run.Err() != nil && ctx.Err() == nil {
		v.Phase, v.Message = PhaseAborted, "the experiment ran out of time: "+v.Message
	}
	l.Log.Info("experiment done", "experiment", key, "purpose", exp.Spec.Purpose, "phase", v.Phase, "message", v.Message)
	exp.Status.Fidelity, exp.Status.Runs = v.Fidelity, int32(min(v.Runs, 1<<30)) //nolint:gosec // bounded just above
	return l.setStatus(ctx, &exp, v.Phase, v.Message)
}

func (l *Lab) setStatus(ctx context.Context, exp *v1alpha1.Experiment, phase v1alpha1.ExperimentPhase, msg string) error {
	exp.Status.Phase, exp.Status.Message, exp.Status.ObservedGeneration = phase, msg, exp.Generation
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(exp)
	if err != nil {
		return err
	}
	updated, err := l.Dynamic.Resource(ExperimentsResource).Namespace(exp.Namespace).UpdateStatus(ctx,
		&unstructured.Unstructured{Object: obj}, metav1.UpdateOptions{FieldManager: "sentinel-lab"})
	if err != nil {
		return err
	}
	exp.ResourceVersion = updated.GetResourceVersion()
	return nil
}
