//go:build linux
// +build linux

package logcache

import (
	"fmt"
	"os"
	"syscall"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

func openFileWithRetry(filePath string, flag int) (file *os.File, err error) {
	for {
		file, err = os.OpenFile(filePath, flag, 0)
		if err == nil {
			return file, nil
		}

		if errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.EMFILE) {
			if err = incrementNoFileRLimit(); err != nil {
				break
			}
			continue
		} else if errors.Is(err, syscall.EPERM) {
			flag = flag & ^syscall.O_NOATIME
			continue
		} else {
			break
		}
	}
	return file, err
}

func EvictFileCache(filePath string, fileSizeBytes int64) error {
	file, err := openFileWithRetry(filePath, os.O_RDONLY|syscall.O_NOATIME)
	if err != nil {
		return err
	}

	defer func(file *os.File) {
		_ = file.Close()
	}(file)

	err = unix.Fadvise(int(file.Fd()), 0, fileSizeBytes, unix.FADV_DONTNEED)
	if err != nil {
		return fmt.Errorf("failed to evict page cache for file %s", filePath)
	}

	return nil
}

func incrementNoFileRLimit() error {
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		return err
	}
	rLimit.Cur = rLimit.Max + 1
	rLimit.Max = rLimit.Max + 1

	err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		return err
	}
	return nil
}
