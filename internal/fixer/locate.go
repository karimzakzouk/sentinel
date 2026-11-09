// Package fixer turns a mitigation into a change at the source: it finds
// where, in the files a GitOps owner deploys, the bad value a blamed change
// set is written, and the smallest edit that writes the good one back.
package fixer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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

// Change is the image the blamed change gave a container, and the image
// production ran before it.
type Change struct {
	Container string
	Bad, Good string
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
// container gets its bad image, and returns the edit that writes the good
// image there. root holds either a kustomization, rendered as Kustomize
// renders it, or plain manifests. Rendered again with the edit, the files
// must differ from before only in that container's image.
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
	if image := imageOf(res, c.Container); image != c.Bad {
		return nil, fmt.Errorf("%s renders %s's image as %q, not %s: the bad image is set outside these files, "+
			"such as on the owner itself", rel(root), c.Container, image, c.Bad)
	}
	edit, err := placeImage(repo, root, res, kind, name, c)
	if err != nil {
		return nil, err
	}
	after, err := render(repo, root, kfile, edit)
	if err != nil {
		return nil, fmt.Errorf("render %s with the fix: %w", rel(root), err)
	}
	if err := onlyImageChanged(before, after, kind, name, c); err != nil {
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

// imageOf is a rendered workload's container image, or "".
func imageOf(r *resource.Resource, container string) string {
	m, err := r.Map()
	if err != nil {
		return ""
	}
	if c := containerIn(m, container); c != nil {
		image, _ := c["image"].(string)
		return image
	}
	return ""
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

// placeImage finds what writes the bad image: an images entry in a
// kustomization whose transformer produced it, or else the manifest the
// workload came from.
func placeImage(repo filesys.FileSystem, root string, r *resource.Resource, kind, name string, c Change) (*Edit, error) {
	anns := r.GetAnnotations()
	var origin struct {
		Path string `yaml:"path"`
		Repo string `yaml:"repo"`
	}
	if err := yaml.Unmarshal([]byte(anns[originAnnotation]), &origin); err != nil || origin.Path == "" || origin.Repo != "" {
		return nil, fmt.Errorf("%s %s does not come from a file in this repository", kind, name)
	}
	file := path.Join(root, origin.Path)
	src, err := repo.ReadFile(file)
	if err != nil {
		return nil, err
	}
	node, err := imageNode(src, kind, name, c.Container)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel(file), err)
	}
	if node.Value == c.Bad {
		return spliceEdit(file, src, node, c.Good,
			fmt.Sprintf("%s sets %s's image to %s on line %d", rel(file), c.Container, c.Bad, node.Line))
	}
	var transformers []struct {
		ConfiguredIn string `yaml:"configuredIn"`
		ConfiguredBy struct {
			Kind string `yaml:"kind"`
		} `yaml:"configuredBy"`
	}
	_ = yaml.Unmarshal([]byte(anns[transformationsAnnotation]), &transformers)
	type match struct {
		file  string
		src   []byte
		entry *yaml.Node
	}
	var matches []match
	var seen []string
	for _, t := range transformers {
		kf := path.Join(root, t.ConfiguredIn)
		if t.ConfiguredBy.Kind != "ImageTagTransformer" || slices.Contains(seen, kf) {
			continue
		}
		seen = append(seen, kf)
		ksrc, err := repo.ReadFile(kf)
		if err != nil {
			return nil, err
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(ksrc, &doc); err != nil || len(doc.Content) == 0 {
			continue
		}
		images := valueOf(doc.Content[0], "images")
		if images == nil {
			continue
		}
		for _, e := range images.Content {
			if applyImage(e, node.Value) == c.Bad {
				matches = append(matches, match{kf, ksrc, e})
			}
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("%d images entries turn %s's image %s into %s; the Fixer needs exactly one", len(matches), c.Container, node.Value, c.Bad)
	}
	m := matches[0]
	goodName, goodTag, goodDigest := splitImage(c.Good)
	badName, _, _ := splitImage(c.Bad)
	if goodName != badName {
		return nil, fmt.Errorf("the image before the change, %s, is not %s at another version", c.Good, badName)
	}
	where := fmt.Sprintf("%s's images entry for %s", rel(m.file), scalar(m.entry, "name"))
	switch tag, digest := valueOf(m.entry, "newTag"), valueOf(m.entry, "digest"); {
	case digest != nil && goodDigest != "":
		return spliceEdit(m.file, m.src, digest, goodDigest, fmt.Sprintf("%s sets the digest %s on line %d", where, digest.Value, digest.Line))
	case tag != nil && digest == nil && goodTag != "" && goodDigest == "":
		return spliceEdit(m.file, m.src, tag, goodTag, fmt.Sprintf("%s sets the tag %s on line %d", where, tag.Value, tag.Line))
	}
	return nil, fmt.Errorf("%s cannot be set back to %s by changing one value", where, c.Good)
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

// imageNode finds, in a file of YAML documents, the image of a workload's
// container.
func imageNode(src []byte, kind, name, container string) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var found []*yaml.Node
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if len(doc.Content) == 0 {
			continue
		}
		top := doc.Content[0]
		if scalar(top, "kind") != kind || scalar(valueOf(top, "metadata"), "name") != name {
			continue
		}
		podSpec := valueOf(valueOf(valueOf(top, "spec"), "template"), "spec")
		for _, key := range []string{"containers", "initContainers"} {
			list := valueOf(podSpec, key)
			if list == nil {
				continue
			}
			for _, c := range list.Content {
				if scalar(c, "name") == container {
					if image := valueOf(c, "image"); image != nil {
						found = append(found, image)
					}
				}
			}
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("%d %ss named %s set container %s's image, not one", len(found), kind, name, container)
	}
	return found[0], nil
}

func valueOf(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalar(m *yaml.Node, key string) string {
	if v := valueOf(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

// onlyImageChanged checks that rendering with the fix differs from before
// only in the target container's image, which is now the good one.
func onlyImageChanged(before, after resmap.ResMap, kind, name string, c Change) error {
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
		if b.GetKind() == kind && b.GetName() == name {
			container := containerIn(bm, c.Container)
			if got := imageOf(a, c.Container); got != c.Good || container == nil {
				return fmt.Errorf("with the fix %s's image renders as %q, not %s", c.Container, got, c.Good)
			}
			container["image"] = c.Good
		}
		if !reflect.DeepEqual(bm, am) {
			return fmt.Errorf("the fix changes %s %s beyond %s's image", a.GetKind(), a.GetName(), c.Container)
		}
	}
	return nil
}

// locatePlain finds the bad image in a directory of plain manifests, as
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
		node, err := imageNode(src, kind, name, c.Container)
		if err != nil || node.Value != c.Bad {
			return nil
		}
		edit, err := spliceEdit(p, src, node, c.Good, fmt.Sprintf("%s sets %s's image to %s on line %d", rel(p), c.Container, c.Bad, node.Line))
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
		return nil, fmt.Errorf("%d manifests under %s set %s %s's container %s to %s; the Fixer needs exactly one", len(edits), rel(root), kind, name, c.Container, c.Bad)
	}
	node, err := imageNode(edits[0].After, kind, name, c.Container)
	if err != nil || node.Value != c.Good {
		return nil, fmt.Errorf("%s does not read back with the fix", edits[0].Path)
	}
	return edits[0], nil
}

func rel(p string) string {
	if p = strings.TrimPrefix(path.Clean(p), "/"); p == "" {
		return "."
	}
	return p
}
