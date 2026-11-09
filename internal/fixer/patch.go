package fixer

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// patchFields are the kustomization fields that hold patches: each entry a
// file's path or the patch itself, a strategic merge patch or a JSON 6902
// one.
var patchFields = []string{"patches", "patchesStrategicMerge", "patchesJson6902"}

// patchEdits returns, for each patch of the kustomizations that transformed
// the workload, the edit that stops it setting the change's bad value: in
// the patch's own file, or in the kustomization for a patch written there.
// A patch may name the workload by a target rather than its own name, so
// an edit is only a candidate; the caller keeps the one whose render
// changes nothing else.
func patchEdits(repo filesys.FileSystem, root string, r *resource.Resource, kind string, c Change) []*Edit {
	s, ok := containerSlot(r, c.Container)
	if !ok {
		return nil
	}
	var out []*Edit
	var seen []string
	for _, t := range transformers(r) {
		kf := path.Join(root, t.ConfiguredIn)
		if !strings.HasPrefix(t.ConfiguredBy.Kind, "Patch") || slices.Contains(seen, kf) {
			continue
		}
		seen = append(seen, kf)
		src, err := repo.ReadFile(kf)
		if err != nil {
			continue
		}
		docs := documents(src)
		if len(docs) == 0 {
			continue
		}
		for _, field := range patchFields {
			key, list := keyOf(docs[0], field)
			for _, item := range seqItems(list) {
				if edit := entryEdit(repo, kf, src, key, list, item, kind, s, c); edit != nil {
					out = append(out, edit)
				}
			}
		}
	}
	return out
}

// entryEdit edits one patch entry of a kustomization, or nil when the patch
// does not set the bad value.
func entryEdit(repo filesys.FileSystem, kf string, src []byte, key, list, item *yaml.Node, kind string, s slot, c Change) *Edit {
	var file string
	var inline *yaml.Node
	switch item.Kind {
	case yaml.ScalarNode:
		// patchesStrategicMerge holds paths, or patches written in place.
		if strings.Contains(item.Value, "\n") {
			inline = item
		} else {
			file = item.Value
		}
	case yaml.MappingNode:
		file, inline = scalar(item, "path"), valueOf(item, "patch")
	}
	switch {
	case file != "":
		full := path.Join(path.Dir(kf), file)
		text, err := repo.ReadFile(full)
		if err != nil {
			return nil
		}
		after, line, empty, err := editPatch(text, kind, s, c)
		if err != nil {
			return nil
		}
		why := fmt.Sprintf("the patch %s sets %s's %s to %s on line %d", rel(full), c.Container, what(c.field()), shown(c.Bad), line)
		if empty {
			// The patch had nothing else to do: it stays, doing nothing,
			// for a person to delete with whatever else refers to it.
			after = []byte("[]\n")
		}
		return &Edit{Path: rel(full), Line: line, Before: text, After: after, Why: why}
	case inline != nil && inline.Kind == yaml.ScalarNode:
		inner, line, empty, err := editPatch([]byte(inline.Value), kind, s, c)
		if err != nil {
			return nil
		}
		// A block's lines end where the key or list item that holds it does.
		col := item.Column - 2
		if k, _ := keyOf(item, "patch"); k != nil {
			col = k.Column
		}
		var after []byte
		switch {
		case empty:
			after, err = removeEntry(src, key, list, item)
			line = item.Line
		case inline.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
			after, err = replaceBlock(src, inline.Line, col, inner)
			line += inline.Line
		default:
			after, err = splice(src, inline, strings.TrimSuffix(string(inner), "\n"))
			line = inline.Line
		}
		if err != nil {
			return nil
		}
		return &Edit{Path: rel(kf), Line: line, Before: src, After: after,
			Why: fmt.Sprintf("a patch in %s sets %s's %s to %s on line %d", rel(kf), c.Container, what(c.field()), shown(c.Bad), line)}
	}
	return nil
}

// slot is where a container sits in the rendered workload, for the paths a
// JSON 6902 patch names it by, and the order of its environment variables.
type slot struct {
	list  string
	index int
	env   []string
}

func containerSlot(r *resource.Resource, name string) (slot, bool) {
	m, err := r.Map()
	if err != nil {
		return slot{}, false
	}
	spec, _ := m["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	podSpec, _ := tmpl["spec"].(map[string]any)
	for _, list := range []string{"containers", "initContainers"} {
		items, _ := podSpec[list].([]any)
		for i, item := range items {
			c, _ := item.(map[string]any)
			if c == nil || c["name"] != name {
				continue
			}
			s := slot{list: list, index: i}
			for _, e := range envOf(c) {
				n, _ := e["name"].(string)
				s.env = append(s.env, n)
			}
			return s, true
		}
	}
	return slot{}, false
}

// editPatch edits a patch's text so that it no longer sets the change's bad
// value: a strategic merge patch as a manifest is edited, a JSON 6902 patch
// by its one operation that sets the value. empty reports a JSON 6902 patch
// left with no operation. line is 1-based within the text.
func editPatch(text []byte, kind string, s slot, c Change) (after []byte, line int, empty bool, err error) {
	docs := documents(text)
	if len(docs) != 1 {
		return nil, 0, false, fmt.Errorf("%d documents in one patch", len(docs))
	}
	switch top := docs[0]; top.Kind {
	case yaml.MappingNode:
		after, line, err = editField(text, kind, "", c)
		return after, line, false, err
	case yaml.SequenceNode:
		return editOps(text, top, s, c)
	}
	return nil, 0, false, fmt.Errorf("not a patch")
}

// How a JSON 6902 operation sets the value: the field itself, an
// environment variable's value, a whole variable, or the whole list of
// them.
type opForm int

const (
	formField opForm = iota
	formEntry
	formEnvList
)

// editOps edits the operation of a JSON 6902 patch that sets the bad value:
// in place when the patch is written as a YAML list, and written again when
// it is JSON on one line.
func editOps(text []byte, ops *yaml.Node, s slot, c Change) ([]byte, int, bool, error) {
	index, form := -1, formField
	for i, op := range ops.Content {
		if f, ok := opMatch(op, s, c); ok {
			if index >= 0 {
				return nil, 0, false, fmt.Errorf("two operations set %s", what(c.field()))
			}
			index, form = i, f
		}
	}
	if index < 0 {
		return nil, 0, false, fmt.Errorf("no operation sets %s to %s", what(c.field()), shown(c.Bad))
	}
	op := ops.Content[index]
	if ops.Style&yaml.FlowStyle != 0 {
		out, empty, err := rewriteOps(text, index, form, c)
		return out, op.Line, empty, err
	}
	gone := c.Good == nil
	key, value := keyOf(op, "value")
	switch {
	case gone && (form != formEnvList || len(value.Content) == 1) && len(ops.Content) == 1:
		return nil, op.Line, true, nil
	case gone && (form != formEnvList || len(value.Content) == 1):
		out, err := deleteLines(text, op.Line, blockEnd(text, op.Line, op.Column-2, false))
		return out, op.Line, false, err
	case form == formField && value.Kind == yaml.SequenceNode:
		out, err := replaceList(text, key, value, c.Good.([]string))
		return out, key.Line, false, err
	case form == formField:
		out, err := splice(text, value, c.Good.(string))
		return out, value.Line, false, err
	case form == formEntry:
		v := valueOf(value, "value")
		out, err := splice(text, v, c.Good.(string))
		return out, v.Line, false, err
	}
	entry := envEntry(value, strings.TrimPrefix(c.field(), envPrefix))
	if gone {
		out, err := deleteLines(text, entry.Line, blockEnd(text, entry.Line, entry.Column-2, false))
		return out, entry.Line, false, err
	}
	v := valueOf(entry, "value")
	out, err := splice(text, v, c.Good.(string))
	return out, v.Line, false, err
}

// opMatch reports whether a JSON 6902 operation adds or replaces the
// change's bad value in the target container, and how.
func opMatch(op *yaml.Node, s slot, c Change) (opForm, bool) {
	if verb := scalar(op, "op"); verb != "add" && verb != "replace" {
		return 0, false
	}
	rest, ok := strings.CutPrefix(scalar(op, "path"), "/spec/template/spec/"+s.list+"/"+strconv.Itoa(s.index)+"/")
	value := valueOf(op, "value")
	if !ok || value == nil {
		return 0, false
	}
	field := c.field()
	switch {
	case field == fieldImage:
		return formField, rest == fieldImage && value.Kind == yaml.ScalarNode && value.Value == c.Bad
	case field == fieldCommand || field == fieldArgs:
		bad, _ := c.Bad.([]string)
		return formField, rest == field && value.Kind == yaml.SequenceNode && slices.Equal(seqStrings(value), bad)
	case strings.HasPrefix(field, envPrefix):
		name := strings.TrimPrefix(field, envPrefix)
		parts := strings.Split(rest, "/")
		switch {
		case len(parts) == 3 && parts[0] == "env" && parts[2] == "value":
			j, err := strconv.Atoi(parts[1])
			return formField, err == nil && j < len(s.env) && s.env[j] == name && value.Kind == yaml.ScalarNode && value.Value == c.Bad
		case len(parts) == 2 && parts[0] == "env":
			return formEntry, value.Kind == yaml.MappingNode && scalar(value, "name") == name && scalar(value, "value") == c.Bad
		case rest == "env":
			e := envEntry(value, name)
			return formEnvList, e != nil && scalar(e, "value") == c.Bad
		}
	}
	return 0, false
}

func envEntry(list *yaml.Node, name string) *yaml.Node {
	for _, e := range seqItems(list) {
		if scalar(e, "name") == name {
			return e
		}
	}
	return nil
}

func seqStrings(n *yaml.Node) []string {
	out := []string{}
	for _, item := range n.Content {
		out = append(out, item.Value)
	}
	return out
}

// rewriteOps edits a JSON 6902 patch written as JSON: the operation at
// index is changed or dropped, and the patch written again on one line, in
// its own spacing.
func rewriteOps(text []byte, index int, form opForm, c Change) ([]byte, bool, error) {
	var ops []map[string]any
	if err := yaml.Unmarshal(text, &ops); err != nil {
		return nil, false, err
	}
	op := ops[index]
	name := strings.TrimPrefix(c.field(), envPrefix)
	switch {
	case form == formEnvList:
		entries, _ := op["value"].([]any)
		for i, e := range entries {
			m, _ := e.(map[string]any)
			if m == nil || m["name"] != name {
				continue
			}
			if c.Good == nil {
				entries = slices.Delete(entries, i, i+1)
			} else {
				m["value"] = c.Good
			}
			break
		}
		op["value"] = entries
		if len(entries) == 0 {
			ops = slices.Delete(ops, index, index+1)
		}
	case c.Good == nil:
		ops = slices.Delete(ops, index, index+1)
	case form == formEntry:
		op["value"].(map[string]any)["value"] = c.Good
	default:
		op["value"] = c.Good
	}
	if len(ops) == 0 {
		return nil, true, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	if len(lines) > 1 {
		// JSON written over lines, as a patch file is: in its indentation.
		indent := lines[1][:len(lines[1])-len(strings.TrimLeft(lines[1], " \t"))]
		enc.SetIndent("", cmp.Or(indent, "  "))
	}
	if err := enc.Encode(ops); err != nil {
		return nil, false, err
	}
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(lines) == 1 && (bytes.Contains(text, []byte(", ")) || bytes.Contains(text, []byte(": "))) {
		out = spaced(out)
	}
	if bytes.HasSuffix(text, []byte("\n")) {
		out = append(out, '\n')
	}
	return out, false, nil
}

// spaced puts a space after each comma and colon of compact JSON, outside
// its strings.
func spaced(compact []byte) []byte {
	var out []byte
	inString, escaped := false, false
	for _, b := range compact {
		out = append(out, b)
		switch {
		case escaped:
			escaped = false
		case inString && b == '\\':
			escaped = true
		case b == '"':
			inString = !inString
		case !inString && (b == ',' || b == ':'):
			out = append(out, ' ')
		}
	}
	return out
}

// replaceBlock writes text in place of the lines of a block scalar whose
// indicator is on line, held by a key or list item at 1-based column col,
// at the block's own indentation.
func replaceBlock(src []byte, line, col int, text []byte) ([]byte, error) {
	lines := bytes.SplitAfter(src, []byte("\n"))
	start, end := line+1, blockEnd(src, line, col, false)
	indent := ""
	for i := start; i < end && i <= len(lines); i++ {
		if l := strings.TrimRight(string(lines[i-1]), "\n"); strings.TrimSpace(l) != "" {
			indent = l[:len(l)-len(strings.TrimLeft(l, " "))]
			break
		}
	}
	if indent == "" {
		return nil, fmt.Errorf("the block on line %d holds no lines", line)
	}
	var block []string
	for _, l := range strings.Split(strings.TrimSuffix(string(text), "\n"), "\n") {
		if l == "" {
			block = append(block, "\n")
			continue
		}
		block = append(block, indent+l+"\n")
	}
	return joinLines(lines, start, end, block), nil
}

// removeEntry removes a patch from a kustomization's list, and the list's
// key with it when it was the only one.
func removeEntry(src []byte, key, list, item *yaml.Node) ([]byte, error) {
	if len(list.Content) == 1 {
		return deleteLines(src, key.Line, blockEnd(src, key.Line, key.Column, true))
	}
	return deleteLines(src, item.Line, blockEnd(src, item.Line, item.Column-2, false))
}
