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

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	sysNodeDirectory = "/sys/devices/system/node"
)

func getNUMADistanceMap() (map[int][]NumaDistanceInfo, error) {
	fInfos, err := ioutil.ReadDir(sysNodeDirectory)
	if err != nil {
		return nil, fmt.Errorf("faield to ReadDir /sys/devices/system/node, err %s", err)
	}

	numaDistanceArray := make(map[int][]NumaDistanceInfo)
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
		var distanceArray []NumaDistanceInfo
		for id, distanceStr := range distances {
			distance, err := strconv.Atoi(distanceStr)
			if err != nil {
				return nil, err
			}
			distanceArray = append(distanceArray, NumaDistanceInfo{
				NumaID:   id,
				Distance: distance,
			})
		}
		numaDistanceArray[nodeID] = distanceArray
	}

	return numaDistanceArray, nil
}
