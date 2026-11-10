package lab

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/capture"
)

// FromBodies turns captured non-GET body samples into Experiment writes, at
// most n, each distinct method and path once, most recent first.
func FromBodies(samples []capture.BodySample, n int) []v1alpha1.WriteReplay {
	type key struct{ method, path string; port int }
	seen := map[key]bool{}
	var out []v1alpha1.WriteReplay
	for i := len(samples) - 1; i >= 0; i-- {
		s := samples[i]
		if s.Method == "" || s.Method == http.MethodGet || s.Method == http.MethodHead || !replayableWritePath(s.Path) {
			continue
		}
		k := key{s.Method, s.Path, s.Port}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v1alpha1.WriteReplay{
			Method: s.Method, Path: s.Path, Port: int32(s.Port), Body: s.Body,
			Status: int32(s.Status), //nolint:gosec // status the agent saw, below 1000
		})
		if n >= 0 && len(out) >= n {
			break
		}
	}
	slices.Reverse(out)
	return out
}

// replayableWritePath is a path a Lab can send as it is: absolute, no longer
// than an Experiment allows, without a fragment or a space.
func replayableWritePath(p string) bool {
	return strings.HasPrefix(p, "/") && len(p) <= 1024 && !strings.ContainsAny(p, "?# \t\r\n")
}

// bodiesForSeed turns Experiment writes into body samples for seeding notes.
func bodiesForSeed(writes []v1alpha1.WriteReplay) []capture.BodySample {
	out := make([]capture.BodySample, len(writes))
	for i, w := range writes {
		out[i] = capture.BodySample{Method: w.Method, Path: w.Path, Body: w.Body, Status: int(w.Status)}
	}
	return out
}

// SeedNotes says what captured writes would seed in empty datastores; the Lab
// does not seed yet, only records the hook.
func SeedNotes(samples []capture.BodySample) []string {
	if len(samples) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d captured writes could seed empty datastores; seeding is not run yet", len(samples))}
}

// WriteFidelity notes how many writes each run sends and how statuses are
// judged; emptyDatastores forgives server errors when the response matched
// production's class.
func WriteFidelity(writes []v1alpha1.WriteReplay, emptyDatastores bool) string {
	switch {
	case len(writes) == 0:
		return ""
	case emptyDatastores:
		return fmt.Sprintf("each copy is sent %d writes production made, and must answer each with the same "+
			"status; with datastores empty, a server error is not held against it", len(writes))
	default:
		return fmt.Sprintf("each copy is sent %d writes production made, and must answer each with the same status",
			len(writes))
	}
}

// writeReplayPod sends every write to the copy at ip at once, and writes each
// one's status, 0 when nothing answered, as a line of its termination
// message. Method, URL and body are arguments, never part of the script.
func writeReplayPod(ns, name, ip string, writes []v1alpha1.WriteReplay) *corev1.Pod {
	const script = `set +e; : > /dev/termination-log; i=0
while [ $# -ge 3 ]; do
  m="$1"; u="$2"; b="$3"; shift 3
  case "$m" in
    GET|HEAD)
      s=$(wget -S -q -O /dev/null -T 5 "$u" 2>&1 | grep -o 'HTTP/[0-9.]* [0-9][0-9][0-9]' | sed -n '$p') ;;
    POST)
      s=$(wget --post-data="$b" -S -q -O /dev/null -T 5 "$u" 2>&1 | grep -o 'HTTP/[0-9.]* [0-9][0-9][0-9]' | sed -n '$p') ;;
    *)
      s=$(wget --method="$m" --body-data="$b" -S -q -O /dev/null -T 5 "$u" 2>&1 | grep -o 'HTTP/[0-9.]* [0-9][0-9][0-9]' | sed -n '$p') ;;
  esac
  c=${s##* }; echo "$i ${c:-0}" >> /dev/termination-log
  i=$((i+1))
done
wait`
	args := []string{"sh", "-c", script, "writereplay"}
	for _, w := range writes {
		args = append(args, w.Method, "http://"+hostPort(ip, int(w.Port))+w.Path, w.Body)
	}
	pod := labPod(ns, name, nil, args)
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
	}
	return pod
}

// writesAnswered reports whether a copy answered every write replay with the
// status production saw, forgiving server errors where empty datastores may
// lack data.
func writesAnswered(codes []int, writes []v1alpha1.WriteReplay, serverErrors bool) bool {
	if len(codes) != len(writes) {
		return false
	}
	for i, c := range codes {
		if c == 0 {
			return false
		}
		want := int(writes[i].Status)
		if c == want {
			continue
		}
		if serverErrors && c >= 500 && want >= 500 {
			continue
		}
		return false
	}
	return true
}

// WriteCompareNotes lists each replayed write whose status differed from
// production's, for fidelity and verdict messaging.
func WriteCompareNotes(codes []int, writes []v1alpha1.WriteReplay) []string {
	var out []string
	for i, c := range codes {
		if i >= len(writes) {
			break
		}
		want := int(writes[i].Status)
		if c == want || c == 0 {
			continue
		}
		w := writes[i]
		out = append(out, fmt.Sprintf("write %s %s returned %d, production answered %d", w.Method, w.Path, c, want))
	}
	return out
}

// withWritePorts gives each write without a port the copy's first declared
// one, and drops those it has none for.
func withWritePorts(writes []v1alpha1.WriteReplay, copied *corev1.Pod) []v1alpha1.WriteReplay {
	var first int32
	for _, c := range copied.Spec.Containers {
		for _, p := range c.Ports {
			if first == 0 && (p.Protocol == "" || p.Protocol == corev1.ProtocolTCP) {
				first = p.ContainerPort
			}
		}
	}
	var out []v1alpha1.WriteReplay
	for _, w := range writes {
		if w.Port == 0 {
			w.Port = first
		}
		if w.Port != 0 {
			out = append(out, w)
		}
	}
	return out
}

// writeReplayCodes reads the write replay's lines: each request's status, by index.
func writeReplayCodes(message string, n int) ([]int, error) {
	return replayCodes(message, n)
}
