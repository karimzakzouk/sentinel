package codefix

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

// placeholder is what masking put in place of a value: <num>, <hex>, <ip>.
var placeholder = regexp.MustCompile(`<[a-z]+>`)

// minLogged is the shortest piece of an error line looked for in source:
// shorter pieces match too much.
const minLogged = 12

// LoggedAt finds where the source logs each error line, for a failure that
// left no stack: the longest piece of the line that masking kept, in a line
// of source, is a frame there. A piece found in more than three places says
// nothing and is dropped. It returns the frames, innermost first in the
// order the errors came, and the language of the files they are in.
func LoggedAt(errors []string, files map[string][]byte) ([]logs.Frame, string) {
	var frames []logs.Frame
	language := ""
	seen := map[string]bool{}
	sources := sortedSources(files)
	for _, e := range errors {
		var found []logs.Frame
		// A logger prefixes its own words, a level or a name, which the
		// source's format string lacks: they are dropped one by one.
		for _, piece := range trimmedPieces(longestPiece(e)) {
			found = found[:0]
			for _, name := range sources {
				for i, line := range strings.Split(string(files[name]), "\n") {
					if strings.Contains(line, piece) {
						found = append(found, logs.Frame{File: name, Line: i + 1, InApp: true})
					}
				}
			}
			if len(found) > 0 {
				break
			}
		}
		if len(found) == 0 || len(found) > 3 {
			continue
		}
		for _, f := range found {
			key := fmt.Sprint(f.File, ":", f.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			frames = append(frames, f)
			if language == "" {
				language = languageOf(f.File)
			}
		}
	}
	return frames, language
}

// longestPiece is the longest run of an error template between masked
// values, trimmed of punctuation a format string would not hold.
func longestPiece(template string) string {
	best := ""
	for _, p := range placeholder.Split(template, -1) {
		p = strings.Trim(p, " :=,;\"'()[]{}")
		if len(p) > len(best) {
			best = p
		}
	}
	return best
}

// trimmedPieces are a piece and the same with its first words dropped, one
// by one, as long as they stay long enough to mean something.
func trimmedPieces(piece string) []string {
	var out []string
	words := strings.Fields(piece)
	for i := 0; i < len(words) && i < 4; i++ {
		p := strings.Join(words[i:], " ")
		if len(p) < minLogged {
			break
		}
		out = append(out, p)
	}
	return out
}

// sortedSources are the repository's files any fixer reads.
func sortedSources(files map[string][]byte) []string {
	var exts []string
	for _, e := range languageFiles {
		exts = append(exts, e...)
	}
	exts = append(exts, ".go")
	return sortedFiles(files, exts)
}

// languageOf is the runtime whose crashes a file's code would print.
func languageOf(file string) string {
	ext := path.Ext(file)
	if ext == ".go" {
		return "go"
	}
	var langs []string
	for lang, exts := range languageFiles {
		for _, e := range exts {
			if e == ext {
				langs = append(langs, lang)
			}
		}
	}
	sort.Strings(langs)
	if len(langs) == 0 {
		return ""
	}
	return langs[0]
}
