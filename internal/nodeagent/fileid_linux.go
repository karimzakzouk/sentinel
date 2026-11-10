//go:build linux

package nodeagent

import (
	"io/fs"
	"syscall"
)

func fileID(info fs.FileInfo) (dev, ino uint64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return uint64(st.Dev), st.Ino //nolint:unconvert // Dev width differs by arch
}
