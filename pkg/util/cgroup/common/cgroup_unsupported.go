//go:build !linux && !windows
// +build !linux,!windows

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
	"fmt"

	"github.com/opencontainers/runc/libcontainer/configs"
)

func ReadTasksFile(file string) ([]string, error) {
	return []string{}, fmt.Errorf("unsupported write file")
}

func GetCgroupParamInt(cgroupPath, cgroupFile string) (int64, error) {
	return 0, fmt.Errorf("unsupported write file")
}

func WriteFileIfChange(dir, file, data string) (error, bool, string) {
	return fmt.Errorf("unsupported write file"), false, ""
}

func IsCPUIdleSupported() bool {
	return false
}

func CheckCgroup2UnifiedMode() bool {
	return false
}

func ApplyCgroupConfigs(cgroupPath string, resources *configs.Resources) error {
	return nil
}
