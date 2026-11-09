package fixer

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Fields a fix restores beside the image: a container's command and
// arguments, an environment variable's literal value ("env:NAME"), and its
// memory limit, which a fix sets to a learned value rather than restores.
const (
	fieldImage       = "image"
	fieldCommand     = "command"
	fieldArgs        = "args"
	fieldMemoryLimit = "limits.memory"
	envPrefix        = "env:"
)

// valueIn reads a field of a container, as a map: a string for the image
// and an environment variable, the list for command and arguments, nil
// where it is not set. An environment variable taken from elsewhere
// (valueFrom) reads as valueFromSet.
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
	case field == fieldMemoryLimit:
		res, _ := container["resources"].(map[string]any)
		limits, _ := res["limits"].(map[string]any)
		if v, ok := limits["memory"]; ok && v != nil {
			return fmt.Sprint(v)
		}
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
	case field == fieldMemoryLimit:
		res, _ := container["resources"].(map[string]any)
		limits, _ := res["limits"].(map[string]any)
		if v == nil {
			delete(limits, "memory")
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
		limits["memory"] = v
	}
}

func sameValue(a, b any) bool { return reflect.DeepEqual(a, b) }

// same reports whether two values of the change's field are equal: memory
// limits as quantities, since "64Mi" and "67108864" are one limit.
func (c Change) same(a, b any) bool {
	if c.field() != fieldMemoryLimit {
		return sameValue(a, b)
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if !aok || !bok {
		return a == nil && b == nil
	}
	aq, aerr := resource.ParseQuantity(as)
	bq, berr := resource.ParseQuantity(bs)
	return aerr == nil && berr == nil && aq.Cmp(bq) == 0
}

// shown is a value as a pull request or a log line says it.
func shown(v any) string {
	switch v := v.(type) {
	case nil:
		return "not set"
	case []string:
		return "[" + strings.Join(v, ", ") + "]"
	}
	return fmt.Sprint(v)
}

// what names a field for people: "image", "arguments", "environment
// variable LEVEL".
func what(field string) string {
	switch {
	case field == fieldArgs:
		return "arguments"
	case field == fieldMemoryLimit:
		return "memory limit"
	case strings.HasPrefix(field, envPrefix):
		return "environment variable " + strings.TrimPrefix(field, envPrefix)
	}
	return field
}

// editField edits, in a file of YAML documents, one field of a workload's
// container from the bad value to the good one: a scalar spliced in place, a
// list rewritten in the style it was written in, a key or an environment
// variable the bad change added removed with its lines. It says where.
func editField(src []byte, kind, name string, c Change) ([]byte, int, error) {
	container, err := containerNode(src, kind, name, c.Container)
	if err != nil {
		return nil, 0, err
	}
	field := c.field()
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
	case field == fieldMemoryLimit:
		node := valueOf(valueOf(valueOf(container, "resources"), "limits"), "memory")
		if node == nil || c.Good == nil {
			return nil, 0, fmt.Errorf("container %s sets no memory limit here", c.Container)
		}
		out, err := splice(src, node, c.Good.(string))
		return out, node.Line, err
	}
	return nil, 0, fmt.Errorf("the Fixer does not restore %s", field)
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

// fileValue reads a field of a workload's container as a file writes it.
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
		if c := containerIn(doc, container); c != nil {
			return valueIn(c, field), true
		}
	}
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
