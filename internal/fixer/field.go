package fixer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Fields a fix restores beside the image: a container's command and
// arguments, an environment variable's literal value ("env:NAME"), its
// readiness, liveness or startup probe (the whole object put back), and its
// memory, CPU or ephemeral-storage limit, which a fix sets to a learned
// value rather than restores, and pod-template or replica fields put back
// from the good revision.
const (
	fieldImage          = "image"
	fieldCommand        = "command"
	fieldArgs           = "args"
	fieldReadinessProbe = "readinessProbe"
	fieldLivenessProbe  = "livenessProbe"
	fieldStartupProbe   = "startupProbe"
	fieldMemoryLimit    = "limits.memory"
	fieldCPULimit       = "limits.cpu"
	fieldDiskLimit      = "limits.ephemeral-storage"
	fieldReplicas       = "replicas"
	fieldTerminationGrace = "terminationGracePeriodSeconds"
	fieldActiveDeadline   = "activeDeadlineSeconds"
	envPrefix           = "env:"
)

func isLimitField(field string) bool {
	return field == fieldMemoryLimit || field == fieldCPULimit || field == fieldDiskLimit
}

func isWorkloadField(field string) bool {
	return field == fieldReplicas
}

func isPodSpecField(field string) bool {
	return field == fieldTerminationGrace || field == fieldActiveDeadline
}

func isProbeField(field string) bool {
	return field == fieldReadinessProbe || field == fieldLivenessProbe || field == fieldStartupProbe
}

// valueIn reads a field of a container, as a map: a string for the image
// and an environment variable, the list for command and arguments, a map
// for a probe, nil where it is not set. An environment variable taken from
// elsewhere (valueFrom) reads as valueFromSet.
func valueIn(container map[string]any, field string) any {
	switch {
	case field == fieldImage:
		if s, ok := container["image"].(string); ok {
			return s
		}
	case field == fieldCommand || field == fieldArgs:
		list, ok := container[field].([]any)
		if !ok {
			return nil
		}
		out := make([]string, 0, len(list))
		for _, v := range list {
			out = append(out, fmt.Sprint(v))
		}
		return out
	case strings.HasPrefix(field, envPrefix):
		name := strings.TrimPrefix(field, envPrefix)
		for _, e := range envOf(container) {
			if e["name"] != name {
				continue
			}
			if _, from := e["valueFrom"]; from {
				return valueFromSet
			}
			s, _ := e["value"].(string)
			return s
		}
	case isLimitField(field):
		res, _ := container["resources"].(map[string]any)
		limits, _ := res["limits"].(map[string]any)
		key := strings.TrimPrefix(field, "limits.")
		if v, ok := limits[key]; ok && v != nil {
			return fmt.Sprint(v)
		}
	case isProbeField(field):
		m, _ := container[field].(map[string]any)
		if m == nil {
			return nil
		}
		return normalizeMap(m)
	}
	return nil
}

// valueFromSet stands for an environment variable whose value comes from a
// ConfigMap, a Secret or a field, which a fix does not restore.
var valueFromSet = struct{ valueFrom bool }{true}

func envOf(container map[string]any) []map[string]any {
	list, _ := container["env"].([]any)
	var out []map[string]any
	for _, item := range list {
		if e, ok := item.(map[string]any); ok {
			out = append(out, e)
		}
	}
	return out
}

// setValue writes a field of a container map; nil removes it.
func setValue(container map[string]any, field string, v any) {
	switch {
	case field == fieldImage:
		container["image"] = v
	case field == fieldCommand || field == fieldArgs:
		if v == nil {
			delete(container, field)
			return
		}
		list := []any{}
		for _, s := range v.([]string) {
			list = append(list, s)
		}
		container[field] = list
	case strings.HasPrefix(field, envPrefix):
		name := strings.TrimPrefix(field, envPrefix)
		list, _ := container["env"].([]any)
		kept := list[:0:0]
		for _, item := range list {
			e, _ := item.(map[string]any)
			if e != nil && e["name"] == name {
				if v == nil {
					continue
				}
				e["value"] = v
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(container, "env")
			return
		}
		container["env"] = kept
	case isLimitField(field):
		res, _ := container["resources"].(map[string]any)
		limits, _ := res["limits"].(map[string]any)
		key := strings.TrimPrefix(field, "limits.")
		if v == nil {
			delete(limits, key)
			return
		}
		if res == nil {
			res = map[string]any{}
			container["resources"] = res
		}
		if limits == nil {
			limits = map[string]any{}
			res["limits"] = limits
		}
		limits[key] = v
	case isProbeField(field):
		if v == nil {
			delete(container, field)
			return
		}
		container[field] = v
	}
}

func sameValue(a, b any) bool { return reflect.DeepEqual(a, b) }

func sameInt(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// same reports whether two values of the change's field are equal: memory
// and CPU limits as quantities, since "64Mi" and "67108864" are one limit,
// and probes with Kubernetes' defaults applied, since the API fills them
// and a manifest often leaves them out.
func (c Change) same(a, b any) bool {
	switch c.field() {
	case fieldMemoryLimit, fieldCPULimit, fieldDiskLimit:
		as, aok := a.(string)
		bs, bok := b.(string)
		if !aok || !bok {
			return a == nil && b == nil
		}
		aq, aerr := resource.ParseQuantity(as)
		bq, berr := resource.ParseQuantity(bs)
		return aerr == nil && berr == nil && aq.Cmp(bq) == 0
	case fieldReplicas, fieldTerminationGrace, fieldActiveDeadline:
		return sameInt(a, b)
	}
	if isProbeField(c.field()) {
		return sameProbe(a, b)
	}
	return sameValue(a, b)
}

// sameProbe reports whether two probe maps are the same probe once the
// API's defaults are filled in.
func sameProbe(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	pa, aok := asProbe(a)
	pb, bok := asProbe(b)
	if !aok || !bok {
		return false
	}
	applyProbeDefaults(pa)
	applyProbeDefaults(pb)
	return equality.Semantic.DeepEqual(pa, pb)
}

func asProbe(v any) (*corev1.Probe, bool) {
	if v == nil {
		return nil, false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var p corev1.Probe
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, false
	}
	return &p, true
}

func applyProbeDefaults(p *corev1.Probe) {
	if p.TimeoutSeconds == 0 {
		p.TimeoutSeconds = 1
	}
	if p.PeriodSeconds == 0 {
		p.PeriodSeconds = 10
	}
	if p.SuccessThreshold == 0 {
		p.SuccessThreshold = 1
	}
	if p.FailureThreshold == 0 {
		p.FailureThreshold = 3
	}
}

// normalizeMap makes a YAML- or JSON-decoded map comparable: whole-number
// float64s become ints, as YAML writes them.
func normalizeMap(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalizeMap(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalizeMap(val)
		}
		return out
	case float64:
		if x == float64(int(x)) {
			return int(x)
		}
		return x
	default:
		return v
	}
}

// shown is a value as a pull request or a log line says it.
func shown(v any) string {
	switch v := v.(type) {
	case nil:
		return "not set"
	case int, int32, int64:
		return fmt.Sprint(v)
	case []string:
		return "[" + strings.Join(v, ", ") + "]"
	case map[string]any:
		b, err := yaml.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return strings.Join(strings.Fields(strings.TrimSpace(string(b))), " ")
	}
	return fmt.Sprint(v)
}

// what names a field for people: "image", "arguments", "environment
// variable LEVEL", "readiness probe".
func what(field string) string {
	switch {
	case field == fieldArgs:
		return "arguments"
	case field == fieldReadinessProbe:
		return "readiness probe"
	case field == fieldLivenessProbe:
		return "liveness probe"
	case field == fieldStartupProbe:
		return "startup probe"
	case field == fieldMemoryLimit:
		return "memory limit"
	case field == fieldCPULimit:
		return "CPU limit"
	case field == fieldDiskLimit:
		return "ephemeral-storage limit"
	case field == fieldReplicas:
		return "replicas"
	case field == fieldTerminationGrace:
		return "termination grace period"
	case field == fieldActiveDeadline:
		return "active deadline"
	case strings.HasPrefix(field, envPrefix):
		return "environment variable " + strings.TrimPrefix(field, envPrefix)
	}
	return field
}

// editField edits, in a file of YAML documents, one field of a workload's
// container from the bad value to the good one: a scalar spliced in place, a
// list rewritten in the style it was written in, a probe mapping rewritten
// the same way, a key or an environment variable the bad change added
// removed with its lines. It says where.
func editField(src []byte, kind, name string, c Change) ([]byte, int, error) {
	field := c.field()
	switch {
	case isWorkloadField(field):
		return editWorkloadField(src, kind, name, c)
	case isPodSpecField(field):
		return editPodSpecField(src, kind, name, c)
	}
	container, err := containerNode(src, kind, name, c.Container)
	if err != nil {
		return nil, 0, err
	}
	switch {
	case field == fieldImage:
		node := valueOf(container, "image")
		if node == nil {
			return nil, 0, fmt.Errorf("container %s sets no image", c.Container)
		}
		out, err := splice(src, node, c.Good.(string))
		return out, node.Line, err
	case field == fieldCommand || field == fieldArgs:
		key, value := keyOf(container, field)
		if key == nil {
			return nil, 0, fmt.Errorf("container %s sets no %s here", c.Container, field)
		}
		if c.Good == nil {
			out, err := deleteLines(src, key.Line, blockEnd(src, key.Line, key.Column, value.Kind == yaml.SequenceNode))
			return out, key.Line, err
		}
		out, err := replaceList(src, key, value, c.Good.([]string))
		return out, key.Line, err
	case strings.HasPrefix(field, envPrefix):
		env := valueOf(container, "env")
		var entry *yaml.Node
		for _, e := range seqItems(env) {
			if scalar(e, "name") == strings.TrimPrefix(field, envPrefix) {
				entry = e
			}
		}
		switch {
		case entry == nil:
			return nil, 0, fmt.Errorf("container %s sets no %s here", c.Container, what(field))
		case c.Good != nil:
			node := valueOf(entry, "value")
			if node == nil {
				return nil, 0, fmt.Errorf("container %s takes %s from elsewhere", c.Container, what(field))
			}
			out, err := splice(src, node, c.Good.(string))
			return out, node.Line, err
		case len(env.Content) == 1:
			key, _ := keyOf(container, "env")
			out, err := deleteLines(src, key.Line, blockEnd(src, key.Line, key.Column, true))
			return out, key.Line, err
		}
		dash := entry.Column - 2
		out, err := deleteLines(src, entry.Line, blockEnd(src, entry.Line, dash, false))
		return out, entry.Line, err
	case isLimitField(field):
		key := strings.TrimPrefix(field, "limits.")
		node := valueOf(valueOf(valueOf(container, "resources"), "limits"), key)
		if node == nil || c.Good == nil {
			return nil, 0, fmt.Errorf("container %s sets no %s here", c.Container, what(field))
		}
		out, err := splice(src, node, c.Good.(string))
		return out, node.Line, err
	case isProbeField(field):
		key, value := keyOf(container, field)
		if key == nil {
			return nil, 0, fmt.Errorf("container %s sets no %s here", c.Container, what(field))
		}
		if c.Good == nil {
			out, err := deleteLines(src, key.Line, blockEnd(src, key.Line, key.Column, false))
			return out, key.Line, err
		}
		good, ok := c.Good.(map[string]any)
		if !ok {
			return nil, 0, fmt.Errorf("container %s's %s is not a mapping", c.Container, what(field))
		}
		out, err := replaceMap(src, key, value, good)
		return out, key.Line, err
	}
	return nil, 0, fmt.Errorf("the Fixer does not restore %s", field)
}

// replaceMap writes a mapping in place of a key's mapping, in the mapping's
// own style: on the key's line when it was written there, one key a line
// otherwise, keeping a comment on the key's line.
func replaceMap(src []byte, key, value *yaml.Node, good map[string]any) ([]byte, error) {
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d holds no mapping", key.Line)
	}
	lines := bytes.SplitAfter(src, []byte("\n"))
	if value.Style&yaml.FlowStyle != 0 {
		if value.Line != key.Line {
			return nil, fmt.Errorf("the mapping on line %d spans lines", key.Line)
		}
		var node yaml.Node
		if err := node.Encode(good); err != nil {
			return nil, err
		}
		node.Style = yaml.FlowStyle
		text, err := yaml.Marshal(&node)
		if err != nil {
			return nil, err
		}
		flow := strings.TrimSpace(string(text))
		line := lines[key.Line-1]
		start := byteOffset(line, value.Column-1)
		if start < 0 || start >= len(line) || line[start] != '{' {
			return nil, fmt.Errorf("line %d does not hold the mapping where the YAML parser places it", key.Line)
		}
		end, depth := -1, 0
		for i := start; i < len(line); i++ {
			switch line[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i + 1
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			return nil, fmt.Errorf("line %d holds no closed mapping", key.Line)
		}
		replaced := string(line[:start]) + flow
		if comment := value.LineComment + key.LineComment; comment != "" {
			replaced += " " + comment
			if bytes.HasSuffix(line, []byte("\n")) {
				replaced += "\n"
			}
		} else {
			replaced += string(line[end:])
		}
		return joinLines(lines, key.Line, key.Line+1, []string{replaced}), nil
	}
	if len(value.Content) == 0 {
		return nil, fmt.Errorf("line %d holds an empty mapping", key.Line)
	}
	indent := strings.Repeat(" ", value.Content[0].Column-1)
	body, err := yaml.Marshal(good)
	if err != nil {
		return nil, err
	}
	var block []string
	for _, l := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		block = append(block, indent+l+"\n")
	}
	return joinLines(lines, key.Line+1, blockEnd(src, key.Line, key.Column, false), block), nil
}

// documents decodes the YAML documents of a file to their top nodes, up to
// the first that does not parse.
func documents(src []byte) []*yaml.Node {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var out []*yaml.Node
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			return out
		}
		if len(doc.Content) > 0 {
			out = append(out, doc.Content[0])
		}
	}
}

// fileValue reads a field of a workload or its container as a file writes it.
func fileValue(src []byte, kind, name, container, field string) (any, bool) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			return nil, false
		}
		meta, _ := doc["metadata"].(map[string]any)
		if doc["kind"] != kind || meta["name"] != name {
			continue
		}
		if isWorkloadField(field) {
			return workloadValueIn(doc, field), true
		}
		if isPodSpecField(field) {
			return podSpecValueIn(doc, field), true
		}
		if c := containerIn(doc, container); c != nil {
			return valueIn(c, field), true
		}
	}
}

// workloadTop finds the top node of a workload document in a file.
func workloadTop(src []byte, kind, name string) (*yaml.Node, error) {
	var found []*yaml.Node
	for _, top := range documents(src) {
		if scalar(top, "kind") != kind || name != "" && scalar(valueOf(top, "metadata"), "name") != name {
			continue
		}
		found = append(found, top)
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("%d %ss named %s in the file, not one", len(found), kind, name)
	}
	return found[0], nil
}

func editWorkloadField(src []byte, kind, name string, c Change) ([]byte, int, error) {
	top, err := workloadTop(src, kind, name)
	if err != nil {
		return nil, 0, err
	}
	node := valueOf(valueOf(top, "spec"), "replicas")
	if node == nil || c.Good == nil {
		return nil, 0, fmt.Errorf("%s sets no %s here", kind, what(c.field()))
	}
	out, err := splice(src, node, fmt.Sprint(c.Good))
	return out, node.Line, err
}

func editPodSpecField(src []byte, kind, name string, c Change) ([]byte, int, error) {
	top, err := workloadTop(src, kind, name)
	if err != nil {
		return nil, 0, err
	}
	podSpec := valueOf(valueOf(valueOf(top, "spec"), "template"), "spec")
	node := valueOf(podSpec, c.field())
	if c.Good == nil {
		if node == nil {
			return nil, 0, fmt.Errorf("%s's pod template sets no %s here", kind, what(c.field()))
		}
		key, _ := keyOf(podSpec, c.field())
		out, err := deleteLines(src, key.Line, blockEnd(src, key.Line, key.Column, false))
		return out, key.Line, err
	}
	if node == nil {
		return nil, 0, fmt.Errorf("%s's pod template sets no %s here", kind, what(c.field()))
	}
	out, err := splice(src, node, fmt.Sprint(c.Good))
	return out, node.Line, err
}

// workloadValueIn reads a workload-level field from a rendered resource.
func workloadValueIn(m map[string]any, field string) any {
	if field != fieldReplicas {
		return nil
	}
	spec, _ := m["spec"].(map[string]any)
	if v, ok := spec["replicas"]; ok {
		return normalizeMap(v)
	}
	return nil
}

// podSpecValueIn reads a pod-template field from a rendered resource.
func podSpecValueIn(m map[string]any, field string) any {
	spec, _ := m["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	podSpec, _ := tmpl["spec"].(map[string]any)
	if v, ok := podSpec[field]; ok {
		return normalizeMap(v)
	}
	return nil
}

// containerNode finds, in a file of YAML documents, a workload's container.
// An empty name matches any workload of the kind, as a patch Kustomize
// aims by a target may name another.
func containerNode(src []byte, kind, name, container string) (*yaml.Node, error) {
	var found []*yaml.Node
	for _, top := range documents(src) {
		if scalar(top, "kind") != kind || name != "" && scalar(valueOf(top, "metadata"), "name") != name {
			continue
		}
		podSpec := valueOf(valueOf(valueOf(top, "spec"), "template"), "spec")
		for _, key := range []string{"containers", "initContainers"} {
			for _, c := range seqItems(valueOf(podSpec, key)) {
				if scalar(c, "name") == container {
					found = append(found, c)
				}
			}
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("%d %ss named %s have a container %s, not one", len(found), kind, name, container)
	}
	return found[0], nil
}

func seqItems(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

func keyOf(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// blockEnd is the 1-based line after the block that starts with a key or a
// list item on line start, at 1-based column col: the first line written at
// col or shallower, ignoring blank and comment lines, and, when the block
// holds a list, its items written at col too.
func blockEnd(src []byte, start, col int, list bool) int {
	lines := strings.Split(string(src), "\n")
	end := start
	for i := start; i < len(lines); i++ {
		text := strings.TrimLeft(lines[i], " ")
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		indent := len(lines[i]) - len(text)
		item := list && strings.HasPrefix(text, "- ")
		if indent < col-1 || indent == col-1 && !item {
			break
		}
		end = i + 1
	}
	return end + 1
}

// deleteLines removes lines from, through to, before end, both 1-based.
func deleteLines(src []byte, from, end int) ([]byte, error) {
	lines := bytes.SplitAfter(src, []byte("\n"))
	if from < 1 || end <= from || end-1 > len(lines) {
		return nil, fmt.Errorf("lines %d to %d are outside the file", from, end-1)
	}
	var out bytes.Buffer
	for i, l := range lines {
		if i+1 < from || i+1 >= end {
			out.Write(l)
		}
	}
	return out.Bytes(), nil
}

// replaceList writes a list of strings in place of a key's list, in the
// list's own style: on the key's line when it was written there, one item a
// line otherwise, keeping a comment on the key's line.
func replaceList(src []byte, key, value *yaml.Node, good []string) ([]byte, error) {
	if value.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("line %d holds no list", key.Line)
	}
	items := make([]string, 0, len(good))
	for _, s := range good {
		style := yaml.Style(0)
		if !plainSafe(s) {
			style = yaml.DoubleQuotedStyle
		}
		text, err := scalarText(s, style)
		if err != nil {
			return nil, err
		}
		items = append(items, text)
	}
	lines := bytes.SplitAfter(src, []byte("\n"))
	if value.Style&yaml.FlowStyle != 0 {
		if value.Line != key.Line {
			return nil, fmt.Errorf("the list on line %d spans lines", key.Line)
		}
		line := lines[key.Line-1]
		start := byteOffset(line, value.Column-1)
		if start < 0 {
			return nil, fmt.Errorf("line %d does not hold the list where the YAML parser places it", key.Line)
		}
		replaced := string(line[:start]) + "[" + strings.Join(items, ", ") + "]"
		if comment := value.LineComment + key.LineComment; comment != "" {
			replaced += " " + comment
		}
		if bytes.HasSuffix(line, []byte("\n")) {
			replaced += "\n"
		}
		return joinLines(lines, key.Line, key.Line+1, []string{replaced}), nil
	}
	first := value.Content[0]
	dash := strings.Repeat(" ", first.Column-3)
	block := make([]string, 0, len(items))
	for _, it := range items {
		block = append(block, dash+"- "+it+"\n")
	}
	return joinLines(lines, key.Line+1, blockEnd(src, key.Line, key.Column, true), block), nil
}

// joinLines replaces 1-based lines from, through to, before end.
func joinLines(lines [][]byte, from, end int, with []string) []byte {
	var out bytes.Buffer
	for i, l := range lines {
		switch {
		case i+1 == from:
			for _, w := range with {
				out.WriteString(w)
			}
			fallthrough
		case i+1 > from && i+1 < end:
			continue
		}
		out.Write(l)
	}
	return out.Bytes()
}
