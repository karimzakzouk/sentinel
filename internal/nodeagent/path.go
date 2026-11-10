package nodeagent

import (
	"path/filepath"
	"strings"
)

// PodLog names a container's log file under the kubelet's /var/log/pods tree:
// <namespace>_<pod>_<uid>/<container>/<N>.log
type PodLog struct {
	Namespace, Pod, UID, Container string
	Path                           string
}

// ParsePodLogPath reads a kubelet pod-log path relative to the log root.
// It accepts absolute paths too when they sit under root.
func ParsePodLogPath(root, path string) (PodLog, bool) {
	rel := path
	if filepath.IsAbs(path) {
		var err error
		rel, err = filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return PodLog{}, false
		}
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 {
		return PodLog{}, false
	}
	dir, container, file := parts[0], parts[1], parts[2]
	if !strings.HasSuffix(file, ".log") && !isRotatedLog(file) {
		return PodLog{}, false
	}
	ns, pod, uid, ok := splitPodDir(dir)
	if !ok || container == "" {
		return PodLog{}, false
	}
	return PodLog{Namespace: ns, Pod: pod, UID: uid, Container: container, Path: path}, true
}

// splitPodDir parses <namespace>_<pod>_<uid>. Namespace and pod are DNS
// labels (no underscores); the UID is the rest after the second underscore.
func splitPodDir(dir string) (ns, pod, uid string, ok bool) {
	i := strings.IndexByte(dir, '_')
	if i <= 0 {
		return "", "", "", false
	}
	j := strings.IndexByte(dir[i+1:], '_')
	if j <= 0 {
		return "", "", "", false
	}
	j += i + 1
	ns, pod, uid = dir[:i], dir[i+1:j], dir[j+1:]
	return ns, pod, uid, ns != "" && pod != "" && uid != ""
}

func isRotatedLog(name string) bool {
	// 0.log.20240101-120000 or 0.log.20240101-120000.gz
	return strings.Contains(name, ".log.")
}
