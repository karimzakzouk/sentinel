package nodeagent

import "time"

// Line is one redacted log line from a container on a node.
type Line struct {
	At        time.Time `json:"at"`
	Namespace string    `json:"namespace"`
	Pod       string    `json:"pod"`
	UID       string    `json:"uid,omitempty"`
	Container string    `json:"container"`
	Text      string    `json:"text"`
}

// CgroupSample is an optional reading of a container's cgroup v2 memory
// files. Peak is zero when memory.peak is missing (kernels before 5.19).
type CgroupSample struct {
	Namespace  string `json:"namespace"`
	Pod        string `json:"pod"`
	UID        string `json:"uid,omitempty"`
	Container  string `json:"container,omitempty"`
	MemoryPeak uint64 `json:"memoryPeak,omitempty"`
	// Pressure is the raw contents of memory.pressure when present.
	Pressure string `json:"pressure,omitempty"`
}

// Batch is what the agent posts to the controller.
type Batch struct {
	Node    string         `json:"node,omitempty"`
	Lines   []Line         `json:"lines,omitempty"`
	Cgroups []CgroupSample `json:"cgroups,omitempty"`
}
