//go:build linux
// +build linux

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

package systemservice

import (
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

type pidFD struct {
	fd int
}

func (p *pidFD) Close() error {
	return unix.Close(p.fd)
}

// openPIDIdentity pins the kernel PID object so its numeric PID cannot be
// reused until the returned handle is closed.
func openPIDIdentity(pid int) (io.Closer, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("pidfd_open: invalid pid %d", pid)
	}
	fd, _, errno := unix.Syscall(unix.SYS_PIDFD_OPEN, uintptr(pid), 0, 0)
	if errno != 0 {
		return nil, fmt.Errorf("pidfd_open pid %d: %w", pid, errno)
	}
	return &pidFD{fd: int(fd)}, nil
}
