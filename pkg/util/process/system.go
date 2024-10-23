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

package process

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
)

// CPUSetParse constructs an integer cpu set from a Linux CPU list formatted string.
// See: http://man7.org/linux/man-pages/man7/cpuset.7.html#FORMATS
func CPUSetParse(s string) (sets.Int, error) {
	if s == "" {
		return sets.Int{}, nil
	}

	b := sets.Int{}
	ranges := strings.Split(s, ",")

	for _, r := range ranges {
		boundaries := strings.Split(r, "-")
		if len(boundaries) == 1 {
			elem, err := strconv.Atoi(boundaries[0])
			if err != nil {
				return sets.Int{}, fmt.Errorf("parse index 0 of 1 boundaries failed: %w", err)
			}

			b.Insert(elem)
		} else if len(boundaries) == 2 {
			start, err := strconv.Atoi(boundaries[0])
			if err != nil {
				return sets.Int{}, fmt.Errorf("parse index 0 of 2 boundaries failed: %w", err)
			}

			end, err := strconv.Atoi(boundaries[1])
			if err != nil {
				return sets.Int{}, fmt.Errorf("parse index 1 of 2 boundaries failed: %w", err)
			}

			for e := start; e <= end; e++ {
				b.Insert(e)
			}
		}
	}

	return b, nil
}

// IsCommandInDState checks if the specified command is in the D (uninterruptible sleep) state.
func IsCommandInDState(command string) bool {
	// Execute the command: ps -aux | grep " D" | grep <command> | grep -v grep
	cmd := exec.Command("sh", "-c", "ps -aux | grep ' D' | grep "+command+" | grep -v grep")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()

	// If the command runs successfully and output is not empty, the process is in D state
	if err == nil && strings.TrimSpace(out.String()) != "" {
		return true
	}
	return false
}
