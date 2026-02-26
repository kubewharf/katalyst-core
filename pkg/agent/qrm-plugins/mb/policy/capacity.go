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

package policy

import (
	"fmt"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func getMBGroupAwareCapacities(groupPercentages map[string]int, fullCapacity int) map[string]int {
	if groupPercentages == nil {
		return nil
	}

	result := make(map[string]int)
	for group, percent := range groupPercentages {
		if percent == 0 || percent >= 100 {
			continue
		}
		result[group] = fullCapacity * percent / 100
	}
	return result
}

func initGroupCapacities(metricFetcher metrictypes.MetricsFetcher,
	numaMBWMap map[int]int64, siblingNumaInfo *machine.SiblingNumaInfo, groupPercentages map[string]int,
) (map[string]int, int, error) {
	// determine the allocatable capacity of mem bandwidth for one 'physical' numa (i.e. domain)
	numaMBWCapacityMap := helper.GetNumaAvgMBWCapacityMap(metricFetcher, numaMBWMap)
	general.Infof("[mbm] get numa mb capacity map %v", numaMBWCapacityMap)
	numaMBWAllocatableMap := helper.GetNumaAvgMBWAllocatableMap(metricFetcher, siblingNumaInfo, numaMBWCapacityMap)
	general.Infof("[mbm] get numa mb allocatable map %v", numaMBWAllocatableMap)
	if len(numaMBWAllocatableMap) == 0 {
		return nil, 0, fmt.Errorf("failed to identify MB allocatable")
	}

	var mbAllocatable int64
	for _, v := range numaMBWAllocatableMap {
		mbAllocatable = v
		break
	}
	// mb_plugin processes mem bandwidth in MB units
	defaultMBDomainCapacity := int(mbAllocatable / 1024 / 1024)
	if defaultMBDomainCapacity < minMBCapacity {
		return nil, 0, fmt.Errorf("invalid domain mb capacity %d as configured", defaultMBDomainCapacity)
	}

	if klog.V(6).Enabled() {
		// to print out numa siblings as they are critical to get proper mb domains
		numaSiblings := siblingNumaInfo.SiblingNumaMap
		for id, siblings := range numaSiblings {
			general.Infof("[mbm] numa %d, siblings %v", id, siblings)
		}
	}

	general.Infof("[mbm] default mb domain allocatable capacity %d MB", defaultMBDomainCapacity)
	general.Infof("[mbm] to apply group customized capacity percentages %v", groupPercentages)

	groupCapacities := getMBGroupAwareCapacities(groupPercentages, defaultMBDomainCapacity)
	general.Infof("[mbm] group customized capacity %v", groupCapacities)

	return groupCapacities, defaultMBDomainCapacity, nil
}
