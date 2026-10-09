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

package common

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// ResolveControllerMount returns the mount root for subsys. On cgroup v2 all
// controllers use the unified hierarchy; on v1 it reads mountinfo so combined
// mounts such as cpu,cpuacct are resolved correctly.
func ResolveControllerMount(subsys string) (ControllerMount, error) {
	if CheckCgroup2UnifiedMode() {
		return resolveControllerMount(subsys, true, nil)
	}

	reader, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return ControllerMount{}, fmt.Errorf("open cgroup mountinfo: %w", err)
	}
	defer func() { _ = reader.Close() }()
	return resolveControllerMount(subsys, false, reader)
}

func resolveControllerMount(subsys string, unified bool, reader io.Reader) (ControllerMount, error) {
	if unified {
		return ControllerMount{Root: CgroupFSMountPoint, Unified: true}, nil
	}
	mounts, err := parseControllerMounts(reader)
	if err != nil {
		return ControllerMount{}, err
	}
	root, ok := mounts[subsys]
	if !ok {
		return ControllerMount{}, fmt.Errorf("%w: %s", ErrControllerMountUnavailable, subsys)
	}
	return ControllerMount{Root: root}, nil
}

func parseControllerMounts(reader io.Reader) (map[string]string, error) {
	mounts := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		separator := strings.Index(line, " - ")
		if separator < 0 {
			return nil, fmt.Errorf("malformed mountinfo line %q", line)
		}
		left, right := strings.Fields(line[:separator]), strings.Fields(line[separator+3:])
		if len(left) < 5 || len(right) < 3 {
			return nil, fmt.Errorf("malformed mountinfo line %q", line)
		}
		if right[0] != "cgroup" {
			continue
		}
		for _, option := range strings.Split(right[2], ",") {
			switch option {
			case CgroupSubsysCPU, CgroupSubsysCPUSet:
				mounts[option] = left[4]
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read cgroup mountinfo: %w", err)
	}
	return mounts, nil
}
