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

package manager

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

// GetTaskSchedWait https://docs.kernel.org/scheduler/sched-stats.html#proc-pid-schedstat
// schedwait unit: nanosecond
func GetTaskSchedWait(pids []int) (map[int]uint64, error) {
	taskSchedWait := make(map[int]uint64)

	for _, pid := range pids {
		taskSchedStatFile := fmt.Sprintf("/proc/%d/schedstat", pid)
		b, err := os.ReadFile(taskSchedStatFile)
		if err != nil {
			klog.Warningf("failed to ReadFile(%s), err %s", taskSchedStatFile, err)
			continue
		}

		schedStatLine := strings.TrimRight(string(b), "\n")

		cols := strings.Fields(schedStatLine)
		if len(cols) < 2 {
			klog.Errorf("invalid %s content with less than 2 cols", schedStatLine)
			continue
		}

		schedWait, err := strconv.ParseUint(cols[1], 10, 64)
		if err != nil {
			klog.Errorf("failed ParseUint(%s) in %s, err %s", cols[1], schedStatLine, err)
			continue
		}

		taskSchedWait[pid] = schedWait
	}

	return taskSchedWait, nil
}
