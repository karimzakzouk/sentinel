package nodeagent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readCgroupSamples looks under cgroupRoot for memory.peak and
// memory.pressure files whose path names a pod UID. Kubectl's cgroup
// layout varies by runtime; a miss is normal and yields nothing.
func readCgroupSamples(cgroupRoot string, pods []PodLog) []CgroupSample {
	if cgroupRoot == "" || len(pods) == 0 {
		return nil
	}
	if _, err := os.Stat(cgroupRoot); err != nil {
		return nil
	}
	want := map[string]PodLog{}
	for _, p := range pods {
		if p.UID == "" {
			continue
		}
		want[p.UID] = p
	}
	if len(want) == 0 {
		return nil
	}
	dirs := findPodCgroups(cgroupRoot, want)
	var out []CgroupSample
	seen := map[string]bool{}
	for uid, dir := range dirs {
		p := want[uid]
		key := uid + "/" + p.Container
		if seen[key] {
			continue
		}
		seen[key] = true
		s := CgroupSample{Namespace: p.Namespace, Pod: p.Pod, UID: p.UID, Container: p.Container}
		if b, err := os.ReadFile(filepath.Join(dir, "memory.peak")); err == nil { //nolint:gosec // under the configured cgroup root
			if n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil {
				s.MemoryPeak = n
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, "memory.pressure")); err == nil { //nolint:gosec // under the configured cgroup root
			s.Pressure = strings.TrimSpace(string(b))
		}
		if s.MemoryPeak > 0 || s.Pressure != "" {
			out = append(out, s)
		}
	}
	return out
}

// findPodCgroups returns one directory per pod UID that holds memory files.
func findPodCgroups(root string, want map[string]PodLog) map[string]string {
	needles := map[string]string{}
	for uid := range want {
		needles[uid] = uid
		needles[strings.ReplaceAll(uid, "-", "_")] = uid
	}
	found := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		name := d.Name()
		var uid string
		for needle, real := range needles {
			if strings.Contains(name, needle) {
				uid = real
				break
			}
		}
		if uid == "" || found[uid] != "" {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, "memory.peak")); err != nil {
			if _, err := os.Stat(filepath.Join(path, "memory.pressure")); err != nil {
				return nil
			}
		}
		found[uid] = path
		if len(found) == len(want) {
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
