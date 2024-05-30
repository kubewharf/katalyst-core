/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"syscall"
)

var AppSyscall Syscaller = &realOS{}

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
