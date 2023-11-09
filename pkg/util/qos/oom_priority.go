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

package qos

import (
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
)

const (
	IgnoreOOMPriorityScore                int = -300
	DefaultReclaimedCoresOOMPriorityScore int = -100
	DefaultSharedCoresOOMPriorityScore    int = 0
	DefaultDedicatedCoresOOMPriorityScore int = 100
	DefaultSystemCoresOOMPriorityScore    int = 200
	TopOOMPriorityScore                   int = 300
)

func AlignOOMPriority(qosLevel string, userSpecifiedScore *int) (oomPriorityScore int) {
	// Adjust OOM priority score if user specified
	if userSpecifiedScore != nil {
		oomPriorityScore = *userSpecifiedScore

		if oomPriorityScore == TopOOMPriorityScore {
			return
		}

		// Adjust OOM Priority if it's out of range
		switch qosLevel {
		case apiconsts.PodAnnotationQoSLevelReclaimedCores:
			if oomPriorityScore < DefaultReclaimedCoresOOMPriorityScore {
				oomPriorityScore = DefaultReclaimedCoresOOMPriorityScore
			} else if oomPriorityScore >= DefaultSharedCoresOOMPriorityScore {
				oomPriorityScore = DefaultSharedCoresOOMPriorityScore - 1
			}
		case apiconsts.PodAnnotationQoSLevelSharedCores:
			if oomPriorityScore < DefaultSharedCoresOOMPriorityScore {
				oomPriorityScore = DefaultSharedCoresOOMPriorityScore
			} else if oomPriorityScore >= DefaultDedicatedCoresOOMPriorityScore {
				oomPriorityScore = DefaultDedicatedCoresOOMPriorityScore - 1
			}
		case apiconsts.PodAnnotationQoSLevelDedicatedCores:
			if oomPriorityScore < DefaultDedicatedCoresOOMPriorityScore {
				oomPriorityScore = DefaultDedicatedCoresOOMPriorityScore
			} else if oomPriorityScore >= DefaultSystemCoresOOMPriorityScore {
				oomPriorityScore = DefaultSystemCoresOOMPriorityScore - 1
			}
		case apiconsts.PodAnnotationQoSLevelSystemCores:
			if oomPriorityScore < DefaultSystemCoresOOMPriorityScore {
				oomPriorityScore = DefaultSystemCoresOOMPriorityScore
			} else if oomPriorityScore >= TopOOMPriorityScore {
				oomPriorityScore = TopOOMPriorityScore - 1
			}
		default:
			// Invalid QoS level, set to minimum value
			oomPriorityScore = DefaultReclaimedCoresOOMPriorityScore
		}
	} else {
		// No user specified score, use default based on QoS level
		switch qosLevel {
		case apiconsts.PodAnnotationQoSLevelReclaimedCores:
			oomPriorityScore = DefaultReclaimedCoresOOMPriorityScore
		case apiconsts.PodAnnotationQoSLevelSharedCores:
			oomPriorityScore = DefaultSharedCoresOOMPriorityScore
		case apiconsts.PodAnnotationQoSLevelDedicatedCores:
			oomPriorityScore = DefaultDedicatedCoresOOMPriorityScore
		case apiconsts.PodAnnotationQoSLevelSystemCores:
			oomPriorityScore = DefaultSystemCoresOOMPriorityScore
		default:
			// Invalid QoS level, set to minimum value
			oomPriorityScore = DefaultReclaimedCoresOOMPriorityScore
		}
	}

	return
}
