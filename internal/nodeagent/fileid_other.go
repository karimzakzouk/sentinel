//go:build !linux

package nodeagent

import "io/fs"

func fileID(info fs.FileInfo) (dev, ino uint64) {
	// Without inode identity, rotation is detected when size shrinks.
	return 0, uint64(info.Size()) //nolint:gosec // size is non-negative
}
