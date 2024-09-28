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

package plan

import "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"

// MBAlloc is the mb allocation plan for one sharing domain (package, or cpu socket in NPS1)
type MBAlloc struct {
	Plan map[task.QoSGroup]map[int]int
}

func (m MBAlloc) GetAllocatedMB() int {
	sum := 0
	for _, ccdMB := range m.Plan {
		for _, mb := range ccdMB {
			sum += mb
		}
	}
	return sum
}

func Merge(plans ...*MBAlloc) *MBAlloc {
	result := &MBAlloc{
		Plan: make(map[task.QoSGroup]map[int]int),
	}

	for _, plan := range plans {
		if plan == nil {
			continue
		}
		for qos, ccdMB := range plan.Plan {
			if _, ok := result.Plan[qos]; !ok {
				result.Plan[qos] = make(map[int]int)
			}

			for ccd, mb := range ccdMB {
				result.Plan[qos][ccd] += mb
			}
		}
	}

	return result
}
