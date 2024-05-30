package utils

import (
	"syscall"
)

var (
	AppSyscall Syscaller = &realOS{}
)

type Syscaller interface {
	Close(fd int) (err error)
	Open(path string, mode int, perm uint32) (fd int, err error)
	Pread(fd int, p []byte, offset int64) (n int, err error)
	Pwrite(fd int, p []byte, offset int64) (n int, err error)
}

type realOS struct{}

func (r realOS) Open(path string, mode int, perm uint32) (fd int, err error) {
	return syscall.Open(path, mode, perm)
}

func (r realOS) Close(fd int) (err error) {
	return syscall.Close(fd)
}

func (r realOS) Pread(fd int, p []byte, offset int64) (n int, err error) {
	return syscall.Pread(fd, p, offset)
}

func (r realOS) Pwrite(fd int, p []byte, offset int64) (n int, err error) {
	return syscall.Pwrite(fd, p, offset)
}

var _ Syscaller = &realOS{}
