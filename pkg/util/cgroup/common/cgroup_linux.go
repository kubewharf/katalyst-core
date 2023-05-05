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

package common

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/opencontainers/runc/libcontainer/cgroups"
)

// CheckCgroup2UnifiedMode return whether it is in cgroupv2 env
func CheckCgroup2UnifiedMode() bool {
	return cgroups.IsCgroup2UnifiedMode()
}

func ReadTasksFile(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		s      = bufio.NewScanner(f)
		result = []string{}
	)

	for s.Scan() {
		if t := s.Text(); t != "" {
			result = append(result, t)
		}
	}
	return result, s.Err()
}

func GetCgroupParamInt(cgroupPath, cgroupFile string) (int64, error) {
	fileName := filepath.Join(cgroupPath, cgroupFile)
	contents, err := ioutil.ReadFile(fileName)
	if err != nil {
		return 0, err
	}

	trimmed := strings.TrimSpace(string(contents))
	if trimmed == "max" {
		return math.MaxInt64, nil
	}

	res, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return res, fmt.Errorf("unable to parse %q as a uint from Cgroup file %q", string(contents), fileName)
	}
	return res, nil
}

// WriteFileIfChange writes data to the cgroup joined by dir and
// file if new data is not equal to the old data and return the old data.
func WriteFileIfChange(dir, file, data string) (error, bool, string) {
	oldData, err := cgroups.ReadFile(dir, file)
	if err != nil {
		return err, false, ""
	}

	if strings.TrimSpace(data) != strings.TrimSpace(oldData) {
		if err := cgroups.WriteFile(dir, file, data); err != nil {
			return err, false, oldData
		} else {
			return nil, true, oldData
		}
	}
	return nil, false, oldData
}

// IsCPUIdleSupported checks if cpu idle supported by
// checking if the cpu.idle interface file exists
func IsCPUIdleSupported() bool {
	_, err := GetKubernetesAnyExistAbsCgroupPath(CgroupSubsysCPU, "cpu.idle")
	return err == nil
}
