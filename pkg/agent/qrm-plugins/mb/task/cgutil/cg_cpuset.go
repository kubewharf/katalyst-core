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

package cgutil

import (
	"path"
	"strconv"
	"strings"

	"github.com/spf13/afero"
)

func GetCPUs(fs afero.Fs, cpusetPath string) ([]int, error) {
	return getValues(fs, cpusetPath, "cpuset.cpus")
}

func GetNumaNodes(fs afero.Fs, cpusetPath string) ([]int, error) {
	return getValues(fs, cpusetPath, "cpuset.mems")
}

func getValues(fs afero.Fs, cpusetPath string, cgfile string) ([]int, error) {
	cgPath := path.Join(cpusetPath, cgfile)
	content, err := afero.ReadFile(fs, cgPath)
	if err != nil {
		return nil, err
	}

	return parse(string(content))
}

func parse(content string) ([]int, error) {
	content = strings.TrimSpace(content)
	result := make([]int, 0)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		vs, err := parseSingleLine(line)
		if err != nil {
			return nil, err
		}
		result = append(result, vs...)
	}
	return result, nil
}

func parseSingleLine(content string) ([]int, error) {
	content = strings.TrimSpace(content)
	segs := strings.Split(content, ",")

	result := make([]int, 0)

	for _, seg := range segs {
		vs, err := parseSingleRange(seg)
		if err != nil {
			return nil, err
		}
		result = append(result, vs...)
	}

	return result, nil
}

func parseSingleRange(content string) ([]int, error) {
	content = strings.TrimSpace(content)
	index := strings.Index(content, "-")
	if index == -1 {
		v, err := parseSingleInt(content)
		if err != nil {
			return nil, err
		}
		return []int{v}, nil
	}

	left, right := content[:index], content[index+1:]
	vLeft, err := parseSingleInt(left)
	if err != nil {
		return nil, err
	}
	vRight, err := parseSingleInt(right)
	if err != nil {
		return nil, err
	}
	result := make([]int, vRight-vLeft+1)
	for i := vLeft; i <= vRight; i++ {
		result[i-vLeft] = i
	}
	return result, nil
}

func parseSingleInt(content string) (int, error) {
	content = strings.TrimSpace(content)
	return strconv.Atoi(content)
}
