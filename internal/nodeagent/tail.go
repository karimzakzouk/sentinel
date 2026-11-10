package nodeagent

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tailState tracks how far a file has been read.
type tailState struct {
	offset int64
	dev    uint64
	ino    uint64
}

// listLogFiles walks root for current *.log files under pod directories.
func listLogFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A pod dir can vanish while we walk; skip it.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".log") || strings.Contains(name, ".log.") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, err
}

// readNewLines returns new lines since the last offset. On first sight of a
// file it seeks to the end so only fresh lines are sent (tests pass fromStart).
func readNewLines(path string, state *tailState, fromStart bool) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // path is under the configured log root
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	dev, ino := fileID(info)
	if state.dev != 0 && (state.dev != dev || state.ino != ino) {
		// Rotated or replaced.
		state.offset = 0
	}
	state.dev, state.ino = dev, ino
	if state.offset == 0 && size > 0 && !fromStart {
		state.offset = size
		return nil, nil
	}
	if state.offset > size {
		state.offset = 0
	}
	if _, err := f.Seek(state.offset, io.SeekStart); err != nil {
		return nil, err
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return lines, err
	}
	off, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return lines, err
	}
	state.offset = off
	return lines, nil
}

// stripCRIPrefix drops the kubelet/CRI log prefix:
// "2024-01-02T03:04:05.678901234Z stdout F message"
func stripCRIPrefix(line string) (at time.Time, text string) {
	parts := strings.SplitN(line, " ", 4)
	if len(parts) < 4 {
		return time.Time{}, line
	}
	if parts[1] != "stdout" && parts[1] != "stderr" {
		return time.Time{}, line
	}
	if parts[2] != "F" && parts[2] != "P" {
		return time.Time{}, line
	}
	if t, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
		at = t
	}
	return at, parts[3]
}
