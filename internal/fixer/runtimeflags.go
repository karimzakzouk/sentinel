package fixer

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// heapFlag is a runtime's setting that sizes its heap within a container's
// memory limit, as a literal environment value: the share of the limit it
// may take, and how to read and write it.
type heapFlag struct {
	envs  []string
	share float64
	find  *regexp.Regexp
	write func(bytes float64) string
}

const mib = 1 << 20

var heapFlags = []heapFlag{
	{envs: []string{"JAVA_TOOL_OPTIONS", "JAVA_OPTS", "_JAVA_OPTIONS"}, share: 0.75, find: regexp.MustCompile(`-Xmx(\d+)([kKmMgG]?)`),
		write: func(b float64) string { return fmt.Sprintf("-Xmx%dm", int64(b/mib)) }},
	{envs: []string{"NODE_OPTIONS"}, share: 0.75, find: regexp.MustCompile(`--max-old-space-size=(\d+)()`),
		write: func(b float64) string { return fmt.Sprintf("--max-old-space-size=%d", int64(b/mib)) }},
	{envs: []string{"GOMEMLIMIT"}, share: 0.9, find: regexp.MustCompile(`^(\d+)(B|KiB|MiB|GiB)?$`),
		write: func(b float64) string { return fmt.Sprintf("%dMiB", int64(b/mib)) }},
}

// runtimeFlags are the changes a new memory limit brings to a container's
// heap settings that no longer fit in it: a JVM's -Xmx and Node's
// --max-old-space-size at three quarters of the limit, Go's GOMEMLIMIT at
// nine tenths, where the container sets them as literal values. A setting
// that already fits stays.
func runtimeFlags(spec corev1.PodSpec, container, limit string) []Change {
	q, err := resource.ParseQuantity(limit)
	if err != nil {
		return nil
	}
	bytes := q.AsApproximateFloat64()
	var out []Change
	for _, c := range spec.Containers {
		if c.Name != container {
			continue
		}
		for _, e := range c.Env {
			if e.ValueFrom != nil {
				continue
			}
			for _, f := range heapFlags {
				if !contains(f.envs, e.Name) {
					continue
				}
				m := f.find.FindStringSubmatchIndex(e.Value)
				if m == nil {
					continue
				}
				n, _ := strconv.ParseFloat(e.Value[m[2]:m[3]], 64)
				have := n * unit(e.Value[m[4]:m[5]], e.Name)
				want := math.Floor(bytes*f.share/mib) * mib
				if have <= bytes*f.share || want <= 0 {
					continue
				}
				out = append(out, Change{Container: container, Field: envPrefix + e.Name, Bad: e.Value,
					Good: e.Value[:m[0]] + f.write(want) + e.Value[m[1]:]})
			}
		}
	}
	return out
}

// unit is a heap setting's unit in bytes: a JVM's k, m and g; Node's
// megabytes; Go's B, KiB, MiB and GiB.
func unit(u, env string) float64 {
	switch strings.ToLower(u) {
	case "k", "kib":
		return 1 << 10
	case "m", "mib":
		return mib
	case "g", "gib":
		return 1 << 30
	case "b":
		return 1
	case "":
		if env == "NODE_OPTIONS" {
			return mib
		}
		return 1
	}
	return 1
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
