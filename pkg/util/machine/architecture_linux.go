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

package machine

import (
	"fmt"
	"syscall"
)

func getCPUArchitecture() (string, error) {
	var utsname syscall.Utsname
	if err := syscall.Uname(&utsname); err != nil {
		return "", fmt.Errorf("uname failed: %w", err)
	}

	machine := make([]byte, 0, len(utsname.Machine))
	for _, c := range utsname.Machine {
		if c == 0 {
			break
		}
		machine = append(machine, byte(c))
	}

	return normalizeCPUArchitecture(string(machine)), nil
}
