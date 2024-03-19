//go:build linux

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

package machine

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	sysNodeDirectory = "/sys/devices/system/node"
)

func GetExtraTopologyInfo(conf *global.MachineInfoConfiguration) (*ExtraTopologyInfo, error) {
	fInfos, err := ioutil.ReadDir(sysNodeDirectory)
	if err != nil {
		return nil, fmt.Errorf("faield to ReadDir /sys/devices/system/node, err %s", err)
	}

	// calculate the sibling NUMA allocatable memory bandwidth by the capacity multiplying the allocatable rate.
	// Now, all the NUMAs have the same memory bandwidth capacity and allocatable
	siblingNumaMBWCapacity := conf.SiblingNumaMemoryBandwidthCapacity
	siblingNumaMBWAllocatable := int64(float64(siblingNumaMBWCapacity) * conf.SiblingNumaMemoryBandwidthAllocatableRate)

	numaDistanceArray := make(map[int][]NumaDistanceInfo)
	siblingNumaMap := make(map[int]sets.Int)
	siblingNumaMBWAllocatableMap := make(map[int]int64)
	siblingNumaMBWCapacityMap := make(map[int]int64)
	for _, fi := range fInfos {
		if !fi.IsDir() {
			continue
		}
		if !strings.HasPrefix(fi.Name(), "node") {
			continue
		}

		nodeID, err := strconv.Atoi(fi.Name()[len("node"):])
		if err != nil {
			general.Infof("/sys/devices/system/node/%s is not a node directory", fi.Name())
			continue
		}

		b, err := ioutil.ReadFile(filepath.Join("/sys/devices/system/node", fi.Name(), "distance"))
		if err != nil {
			return nil, err
		}
		s := strings.TrimSpace(strings.TrimRight(string(b), "\n"))
		distances := strings.Fields(s)
		var (
			distanceArray    []NumaDistanceInfo
			siblingSet       sets.Int
			selfNumaDistance int
		)
		for id, distanceStr := range distances {
			distance, err := strconv.Atoi(distanceStr)
			if err != nil {
				return nil, err
			}

			if id == nodeID {
				selfNumaDistance = distance
				continue
			}

			distanceArray = append(distanceArray, NumaDistanceInfo{
				NumaID:   id,
				Distance: distance,
			})

			// the distance between two different NUMAs is equal to the distance between
			// it and itself are siblings each other
			if distance == selfNumaDistance {
				siblingSet.Insert(id)
			}
		}
		numaDistanceArray[nodeID] = distanceArray
		siblingNumaMap[nodeID] = siblingSet
		siblingNumaMBWAllocatableMap[nodeID] = siblingNumaMBWAllocatable
		siblingNumaMBWCapacityMap[nodeID] = siblingNumaMBWCapacity
	}

	return &ExtraTopologyInfo{
		NumaDistanceMap:              numaDistanceArray,
		SiblingNumaMap:               siblingNumaMap,
		SiblingNumaMBWCapacityMap:    siblingNumaMBWCapacityMap,
		SiblingNumaMBWAllocatableMap: siblingNumaMBWAllocatableMap,
	}, nil
}
