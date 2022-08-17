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

package util

import "github.com/kubewharf/katalyst-core/pkg/util/machine"

// TransformCPUAssignmentFormat transforms cpu assignment string format to cpuset format
func TransformCPUAssignmentFormat(assignment map[uint64]string) map[int]machine.CPUSet {
	res := make(map[int]machine.CPUSet)
	for k, v := range assignment {
		res[int(k)] = machine.MustParse(v)
	}
	return res
}

// CountCPUAssignmentCPUs returns sum of cpus among all numas in assignment
func CountCPUAssignmentCPUs(assignment map[int]machine.CPUSet) int {
	res := 0
	for _, v := range assignment {
		res += v.Size()
	}
	return res
}
