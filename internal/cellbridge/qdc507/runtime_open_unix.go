//go:build !windows

package qdc507

import (
	"os"
	"syscall"
)

func openRuntimeNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
