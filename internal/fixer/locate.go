// Package fixer turns a mitigation into a change at the source: it finds
// where, in the files a GitOps owner deploys, the bad value a blamed change
// set is written, and the smallest edit that writes the good one back.
package fixer

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Target is the workload a fix restores: its kind, its name in the cluster,
// and what its owner adds to the names it renders (Flux's and Argo CD's
// name prefix and suffix).
type Target struct {
	Kind, Name             string
	NamePrefix, NameSuffix string
}

// Change is a value the blamed change gave one of a workload's containers,
// and the value production ran before it.
type Change struct {
	Container string
	// Field is "image" (the default), "command", "args", "env:NAME" for an
	// environment variable's literal value, "readinessProbe",
	// "livenessProbe", "startupProbe", or a resource limit.
	Field string
	// Bad and Good are the values: a string for the image and an
	// environment variable, a []string for command and arguments, a
	// map[string]any for a probe, nil where the field was not set.
	Bad, Good any
}

func (c Change) field() string {
	if c.Field == "" {
		return fieldImage
	}
	return c.Field
}

// Edit is a fix to one file of a repository, with how the Fixer placed it.
type Edit struct {
	Path          string
	Line          int
	Before, After []byte
	Why           string
}

var kustomizationFiles = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

const (
	originAnnotation          = "config.kubernetes.io/origin"
	transformationsAnnotation = "alpha.config.kubernetes.io/transformations"
)

// Locate finds where, under root in a repository's files, the target's
// container gets the change's bad value, and returns the edit that writes
// the good value there. root holds either a kustomization, rendered as
// Kustomize renders it, or plain manifests. Rendered again with the edit,
// the files must differ from before only in that value.
func Locate(repo filesys.FileSystem, root string, t Target, c Change) (*Edit, error) {
	root = path.Join("/", root)
	name := strings.TrimSuffix(strings.TrimPrefix(t.Name, t.NamePrefix), t.NameSuffix)
	for _, f := range kustomizationFiles {
		if repo.Exists(path.Join(root, f)) {
			return locateKustomized(repo, root, path.Join(root, f), t.Kind, name, c)
		}
	}
	return locatePlain(repo, root, t.Kind, name, c)
}

func locateKustomized(repo filesys.FileSystem, root, kfile, kind, name string, c Change) (*Edit, error) {
	before, err := render(repo, root, kfile, nil)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", rel(root), err)
	}
	res, err := find(before, kind, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel(root), err)
	}
	if got := valueOfRes(res, c.Container, c.field()); !c.same(got, c.Bad) {
		return nil, fmt.Errorf("%s renders %s's %s as %s, not %s: the bad value is set outside these files, "+
			"such as on the owner itself", rel(root), c.Container, what(c.field()), shown(got), shown(c.Bad))
	}
	var edit *Edit
	if c.field() == fieldImage {
		edit, err = placeImage(repo, root, res, kind, name, c)
	} else {
		edit, err = placeField(repo, root, kfile, before, res, kind, name, c)
	}
	if err != nil {
		return nil, err
	}
	after, err := render(repo, root, kfile, edit)
	if err != nil {
		return nil, fmt.Errorf("render %s with the fix: %w", rel(root), err)
	}
	if err := onlyChanged(before, after, kind, name, c); err != nil {
		return nil, err
	}
	return edit, nil
}

// render builds root as Kustomize does, from a copy of the repository with
// edit applied and with build metadata turned on, so each resource names
// the file it came from and the kustomizations that transformed it.
func render(repo filesys.FileSystem, root, kfile string, edit *Edit) (resmap.ResMap, error) {
	work, err := copyFS(repo)
	if err != nil {
		return nil, err
	}
	if edit != nil {
		if err := work.WriteFile(path.Join("/", edit.Path), edit.After); err != nil {
			return nil, err
		}
	}
	k, err := work.ReadFile(kfile)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(k, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", rel(kfile), err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	doc["buildMetadata"] = []string{"originAnnotations", "transformerAnnotations"}
	k, err = yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if err := work.WriteFile(kfile, k); err != nil {
		return nil, err
	}
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	return krusty.MakeKustomizer(opts).Run(work, root)
}

func copyFS(repo filesys.FileSystem) (filesys.FileSystem, error) {
	work := filesys.MakeFsInMemory()
	err := repo.Walk("/", func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := repo.ReadFile(p)
		if err != nil {
			return err
		}
		return work.WriteFile(p, data)
	})
	return work, err
}

func find(m resmap.ResMap, kind, name string) (*resource.Resource, error) {
	var found []*resource.Resource
	for _, r := range m.Resources() {
		if r.GetKind() == kind && r.GetName() == name {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("renders %d %ss named %s, not one", len(found), kind, name)
	}
	return found[0], nil
}

// valueOfRes reads a field of a rendered workload or its container.
func valueOfRes(r *resource.Resource, container, field string) any {
	m, err := r.Map()
	if err != nil {
		return nil
	}
	if isWorkloadField(field) {
		return workloadValueIn(m, field)
	}
	if isPodSpecField(field) {
		return podSpecValueIn(m, field)
	}
	if c := containerIn(m, container); c != nil {
		return valueIn(c, field)
	}
	return nil
}

func containerIn(m map[string]any, name string) map[string]any {
	spec, _ := m["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	podSpec, _ := tmpl["spec"].(map[string]any)
	for _, key := range []string{"containers", "initContainers"} {
		list, _ := podSpec[key].([]any)
		for _, item := range list {
			if c, _ := item.(map[string]any); c != nil && c["name"] == name {
				return c
			}
		}
	}
	return nil
}

// origin is the file of the repository a rendered resource came from.
func origin(root string, r *resource.Resource, kind, name string) (string, error) {
	var o struct {
		Path string `yaml:"path"`
		Repo string `yaml:"repo"`
	}
	if err := yaml.Unmarshal([]byte(r.GetAnnotations()[originAnnotation]), &o); err != nil || o.Path == "" || o.Repo != "" {
		return "", fmt.Errorf("%s %s does not come from a file in this repository", kind, name)
	}
	return path.Join(root, o.Path), nil
}

type transformer struct {
	ConfiguredIn string `yaml:"configuredIn"`
	ConfiguredBy struct {
		Kind string `yaml:"kind"`
	} `yaml:"configuredBy"`
}

func transformers(r *resource.Resource) []transformer {
	var ts []transformer
	_ = yaml.Unmarshal([]byte(r.GetAnnotations()[transformationsAnnotation]), &ts)
	return ts
}

// placeImage finds what writes the bad image: an images entry in a
// kustomization whose transformer produced it, or else the manifest the
// workload came from.
func placeImage(repo filesys.FileSystem, root string, r *resource.Resource, kind, name string, c Change) (*Edit, error) {
	bad, good := c.Bad.(string), c.Good.(string)
	file, err := origin(root, r, kind, name)
	if err != nil {
		return nil, err
	}
	src, err := repo.ReadFile(file)
	if err != nil {
		return nil, err
	}
	container, err := containerNode(src, kind, name, c.Container)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel(file), err)
	}
	node := valueOf(container, "image")
	if node == nil {
		return nil, fmt.Errorf("%s sets no image for container %s", rel(file), c.Container)
	}
	if node.Value == bad {
		return spliceEdit(file, src, node, good,
			fmt.Sprintf("%s sets %s's image to %s on line %d", rel(file), c.Container, bad, node.Line))
	}
	type match struct {
		file  string
		src   []byte
		entry *yaml.Node
	}
	var matches []match
	var seen []string
	for _, t := range transformers(r) {
		kf := path.Join(root, t.ConfiguredIn)
		if t.ConfiguredBy.Kind != "ImageTagTransformer" || slices.Contains(seen, kf) {
			continue
		}
		seen = append(seen, kf)
		ksrc, err := repo.ReadFile(kf)
		if err != nil {
			return nil, err
		}
		docs := documents(ksrc)
		if len(docs) == 0 {
			continue
		}
		for _, e := range seqItems(valueOf(docs[0], "images")) {
			if applyImage(e, node.Value) == bad {
				matches = append(matches, match{kf, ksrc, e})
			}
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("%d images entries turn %s's image %s into %s; the Fixer needs exactly one", len(matches), c.Container, node.Value, bad)
	}
	m := matches[0]
	goodName, goodTag, goodDigest := splitImage(good)
	badName, _, _ := splitImage(bad)
	if goodName != badName {
		return nil, fmt.Errorf("the image before the change, %s, is not %s at another version", good, badName)
	}
	where := fmt.Sprintf("%s's images entry for %s", rel(m.file), scalar(m.entry, "name"))
	switch tag, digest := valueOf(m.entry, "newTag"), valueOf(m.entry, "digest"); {
	case digest != nil && goodDigest != "":
		return spliceEdit(m.file, m.src, digest, goodDigest, fmt.Sprintf("%s sets the digest %s on line %d", where, digest.Value, digest.Line))
	case tag != nil && digest == nil && goodTag != "" && goodDigest == "":
		return spliceEdit(m.file, m.src, tag, goodTag, fmt.Sprintf("%s sets the tag %s on line %d", where, tag.Value, tag.Line))
	}
	return nil, fmt.Errorf("%s cannot be set back to %s by changing one value", where, good)
}

// placeField finds a field other than the image: in the manifest the
// workload came from, or in a patch of a kustomization that transformed it.
// Each is a candidate edit, kept only when the render it makes differs from
// before in the change's field alone; the Fixer proposes the one that is
// left, and refuses when none or several are.
func placeField(repo filesys.FileSystem, root, kfile string, before resmap.ResMap, r *resource.Resource, kind, name string, c Change) (*Edit, error) {
	var cands []*Edit
	file, err := origin(root, r, kind, name)
	if err != nil {
		return nil, err
	}
	src, err := repo.ReadFile(file)
	if err != nil {
		return nil, err
	}
	manifest, manifestErr := fieldEdit(file, src, kind, name, c)
	if manifestErr == nil {
		cands = append(cands, manifest)
	}
	cands = append(cands, patchEdits(repo, root, r, kind, c)...)
	var held []*Edit
	for _, edit := range cands {
		if after, err := render(repo, root, kfile, edit); err == nil && onlyChanged(before, after, kind, name, c) == nil {
			held = append(held, edit)
		}
	}
	switch {
	case len(held) == 1 && held[0] != manifest && manifestErr == nil:
		if v, ok := fileValue(src, kind, name, c.Container, c.field()); ok && c.same(v, c.Bad) {
			held[0].Why += fmt.Sprintf("; %s sets the same value under it, which the patch hides", rel(file))
		}
		return held[0], nil
	case len(held) == 1:
		return held[0], nil
	case len(held) > 1:
		return nil, fmt.Errorf("%d edits would each restore %s's %s; the Fixer proposes one only when one place sets it", len(held), c.Container, what(c.field()))
	}
	for _, t := range transformers(r) {
		if strings.HasPrefix(t.ConfiguredBy.Kind, "Patch") {
			return nil, fmt.Errorf("a patch in %s changes %s %s, and no edit of it or of %s restores %s's %s alone",
				rel(path.Join(root, t.ConfiguredIn)), kind, name, rel(file), c.Container, what(c.field()))
		}
	}
	if manifestErr != nil {
		return nil, manifestErr
	}
	return nil, fmt.Errorf("editing %s does not restore %s's %s alone", rel(file), c.Container, what(c.field()))
}

// fieldEdit edits a field in one file, saying where.
func fieldEdit(file string, src []byte, kind, name string, c Change) (*Edit, error) {
	after, line, err := editField(src, kind, name, c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel(file), err)
	}
	who := c.Container + "'s "
	if c.Container == "" {
		who = ""
	}
	return &Edit{Path: rel(file), Line: line, Before: src, After: after,
		Why: fmt.Sprintf("%s sets %s%s to %s on line %d", rel(file), who, what(c.field()), shown(c.Bad), line)}, nil
}

func spliceEdit(file string, src []byte, node *yaml.Node, value, why string) (*Edit, error) {
	after, err := splice(src, node, value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel(file), err)
	}
	return &Edit{Path: rel(file), Line: node.Line, Before: src, After: after, Why: why}, nil
}

// applyImage is what an images entry makes of an image, as Kustomize's
// ImageTagTransformer does, or "" when the entry does not match it.
func applyImage(entry *yaml.Node, image string) string {
	name, tag, digest := splitImage(image)
	if scalar(entry, "name") != name {
		return ""
	}
	if n := scalar(entry, "newName"); n != "" {
		name = n
	}
	switch {
	case scalar(entry, "digest") != "":
		return name + "@" + scalar(entry, "digest")
	case scalar(entry, "newTag") != "":
		return name + ":" + scalar(entry, "newTag")
	case digest != "":
		return name + "@" + digest
	case tag != "":
		return name + ":" + tag
	}
	return name
}

// splitImage splits an image reference into its name, tag and digest.
func splitImage(image string) (name, tag, digest string) {
	name, digest, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
	}
	return name, tag, digest
}

func valueOf(m *yaml.Node, key string) *yaml.Node {
	_, v := keyOf(m, key)
	return v
}

func scalar(m *yaml.Node, key string) string {
	if v := valueOf(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

// onlyChanged checks that rendering with the fix differs from before only in
// the change's field of the target container, which now holds the good
// value.
func onlyChanged(before, after resmap.ResMap, kind, name string, c Change) error {
	if before.Size() != after.Size() {
		return fmt.Errorf("the fix changes which resources render: %d before, %d after", before.Size(), after.Size())
	}
	for i, b := range before.Resources() {
		a := after.Resources()[i]
		bm, err := b.Map()
		if err != nil {
			return err
		}
		am, err := a.Map()
		if err != nil {
			return err
		}
		withoutBuildMetadata(bm)
		withoutBuildMetadata(am)
		if b.GetKind() == kind && b.GetName() == name {
			got := valueOfRes(a, c.Container, c.field())
			if !c.same(got, c.Good) {
				who := c.Container + "'s "
				if c.Container == "" {
					who = ""
				}
				return fmt.Errorf("with the fix %s%s renders as %s, not %s", who, what(c.field()), shown(got), shown(c.Good))
			}
			switch {
			case isWorkloadField(c.field()):
				spec, _ := bm["spec"].(map[string]any)
				if spec == nil {
					return fmt.Errorf("with the fix %s is missing", what(c.field()))
				}
				spec["replicas"] = got
			case isPodSpecField(c.field()):
				spec, _ := bm["spec"].(map[string]any)
				tmpl, _ := spec["template"].(map[string]any)
				podSpec, _ := tmpl["spec"].(map[string]any)
				if podSpec == nil {
					return fmt.Errorf("with the fix %s is missing", what(c.field()))
				}
				podSpec[c.field()] = got
			default:
				container := containerIn(bm, c.Container)
				if container == nil {
					return fmt.Errorf("with the fix %s's %s is missing", c.Container, what(c.field()))
				}
				// Use the rendered value so probe maps match YAML's types, not
				// the API object's JSON round-trip.
				setValue(container, c.field(), got)
			}
		}
		if !reflect.DeepEqual(bm, am) {
			who := c.Container + "'s "
			if c.Container == "" {
				who = ""
			}
			return fmt.Errorf("the fix changes %s %s beyond %s%s", a.GetKind(), a.GetName(), who, what(c.field()))
		}
	}
	return nil
}

// withoutBuildMetadata drops the annotations render asks Kustomize for: a
// fix that removes a patch changes which transformers a resource names,
// not the resource.
func withoutBuildMetadata(m map[string]any) {
	meta, _ := m["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	if ann == nil {
		return
	}
	delete(ann, originAnnotation)
	delete(ann, transformationsAnnotation)
	if len(ann) == 0 {
		delete(meta, "annotations")
	}
}

// locatePlain finds the bad value in a directory of plain manifests, as
// Argo CD and Flux deploy a directory without a kustomization.
func locatePlain(repo filesys.FileSystem, root, kind, name string, c Change) (*Edit, error) {
	var edits []*Edit
	err := repo.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || (path.Ext(p) != ".yaml" && path.Ext(p) != ".yml") {
			return err
		}
		src, err := repo.ReadFile(p)
		if err != nil {
			return err
		}
		if got, ok := fileValue(src, kind, name, c.Container, c.field()); !ok || !c.same(got, c.Bad) {
			return nil
		}
		edit, err := fieldEdit(p, src, kind, name, c)
		if err != nil {
			return err
		}
		edits = append(edits, edit)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(edits) != 1 {
		return nil, fmt.Errorf("%d manifests under %s set %s %s's container %s's %s to %s; the Fixer needs exactly one",
			len(edits), rel(root), kind, name, c.Container, what(c.field()), shown(c.Bad))
	}
	if err := onlyFileChanged(edits[0], kind, name, c); err != nil {
		return nil, err
	}
	return edits[0], nil
}

// onlyFileChanged checks that a plain manifest reads back, with the fix,
// as before but for the change's field, which now holds the good value.
func onlyFileChanged(e *Edit, kind, name string, c Change) error {
	decode := func(src []byte) []map[string]any {
		var docs []map[string]any
		dec := yaml.NewDecoder(bytes.NewReader(src))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				return docs
			}
			docs = append(docs, doc)
		}
	}
	before, after := decode(e.Before), decode(e.After)
	var got any
	for _, doc := range after {
		meta, _ := doc["metadata"].(map[string]any)
		if doc["kind"] == kind && meta["name"] == name {
			if container := containerIn(doc, c.Container); container != nil {
				got = valueIn(container, c.field())
			}
		}
	}
	if !c.same(got, c.Good) {
		return fmt.Errorf("%s reads %s as %s, not %s", e.Path, what(c.field()), shown(got), shown(c.Good))
	}
	for _, doc := range before {
		meta, _ := doc["metadata"].(map[string]any)
		if doc["kind"] == kind && meta["name"] == name {
			if container := containerIn(doc, c.Container); container != nil {
				setValue(container, c.field(), got)
			}
		}
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("%s does not read back with only the fix", e.Path)
	}
	return nil
}

func rel(p string) string {
	if p = strings.TrimPrefix(path.Clean(p), "/"); p == "" {
		return "."
	}
	return p
}
