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

package resctrl

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/moby/sys/mountinfo"
)

func findResctrlMountpointDir() (string, error) {
	// 1. Try mountinfo
	f, err := os.Open("/proc/self/mountinfo")
	if err == nil {
		defer f.Close()
		mi, err := mountinfo.GetMountsFromReader(f, func(m *mountinfo.Info) (bool, bool) {
			if m.FSType == "resctrl" {
				return false, true
			}
			return true, false
		})
		if err == nil && len(mi) > 0 {
			return mi[0].Mountpoint, nil
		}
	}

	// 2. Fallback to default path check
	defaultPath := "/sys/fs/resctrl"
	if _, err := os.Stat(filepath.Join(defaultPath, cpus)); err == nil {
		if _, err := os.Stat(filepath.Join(defaultPath, schemata)); err == nil {
			return defaultPath, nil
		}
	}

	return "", fmt.Errorf("resctrl mountpoint not found")
}
