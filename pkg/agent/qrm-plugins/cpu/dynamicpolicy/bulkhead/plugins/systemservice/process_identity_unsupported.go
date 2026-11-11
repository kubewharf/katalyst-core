//go:build !linux
// +build !linux

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
	"runtime"
)

// openPIDIdentity fails closed on platforms without Linux pidfd support.
func openPIDIdentity(pid int) (io.Closer, error) {
	return nil, fmt.Errorf("pidfd_open pid %d is unsupported on %s", pid, runtime.GOOS)
}
