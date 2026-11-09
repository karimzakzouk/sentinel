package fixer

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// splice replaces the scalar node in src with value, in the scalar's own
// quoting, and leaves every other byte alone: comments, indentation and
// the rest of the line stay as they were. It refuses when src does not
// hold the node's text where the parser says it does.
func splice(src []byte, node *yaml.Node, value string) ([]byte, error) {
	if node.Kind != yaml.ScalarNode || node.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return nil, fmt.Errorf("line %d holds no single-line value", node.Line)
	}
	lines := bytes.SplitAfter(src, []byte("\n"))
	if node.Line < 1 || node.Line > len(lines) {
		return nil, fmt.Errorf("line %d is outside the file", node.Line)
	}
	line := lines[node.Line-1]
	start := byteOffset(line, node.Column-1)
	// A plain scalar is written as its value; a number such as 6291456
	// would come back quoted from a string's marshalling.
	old := node.Value
	if node.Style != 0 {
		var err error
		if old, err = scalarText(node.Value, node.Style); err != nil {
			return nil, err
		}
	}
	if start < 0 || !bytes.HasPrefix(line[start:], []byte(old)) {
		return nil, fmt.Errorf("line %d does not hold %s where the YAML parser places it", node.Line, old)
	}
	style := node.Style
	if style == 0 && !plainSafe(value) {
		style = yaml.DoubleQuotedStyle
	}
	text, err := scalarText(value, style)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for i, l := range lines {
		if i != node.Line-1 {
			out.Write(l)
			continue
		}
		out.Write(l[:start])
		out.WriteString(text)
		out.Write(l[start+len(old):])
	}
	return out.Bytes(), nil
}

// scalarText is how a string scalar is written in a style.
func scalarText(value string, style yaml.Style) (string, error) {
	out, err := yaml.Marshal(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style})
	if err != nil {
		return "", err
	}
	text := strings.TrimSuffix(string(out), "\n")
	if strings.Contains(text, "\n") {
		return "", fmt.Errorf("%q does not fit on one line", value)
	}
	return text, nil
}

// plainSafe reports whether a string reads back as the same string when
// written without quotes; "1.0" or "true" would not.
func plainSafe(value string) bool {
	text, err := scalarText(value, 0)
	return err == nil && text == value
}

// byteOffset is the byte offset of a 0-based character column, or -1.
func byteOffset(line []byte, column int) int {
	for i := range string(line) {
		if column == 0 {
			return i
		}
		column--
	}
	return -1
}
