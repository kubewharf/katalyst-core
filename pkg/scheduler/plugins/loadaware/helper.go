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

package loadaware

import (
	"fmt"
	"math"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type Item struct {
	Value     int64
	Timestamp time.Time
}

type Items []Item

func (it Items) Len() int {
	return len(it)
}

func (it Items) Swap(i, j int) {
	it[i], it[j] = it[j], it[i]
}

func (it Items) Less(i, j int) bool {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.Local
	}
	// sort sample timestamp hour
	houri := it[i].Timestamp.In(location).Hour()
	hourj := it[j].Timestamp.In(location).Hour()

	return houri < hourj
}

func podToWorkloadByOwner(pod *v1.Pod) (string, string, bool) {
	for _, owner := range pod.OwnerReferences {
		kind := owner.Kind
		switch kind {
		// resource portrait time series predicted and stored by deployment, but pod owned by rs
		case "ReplicaSet":
			names := strings.Split(owner.Name, "-")
			if len(names) <= 1 {
				klog.Warningf("unexpected rs name: %v", owner.Name)
				return "", "", false
			}
			names = names[0 : len(names)-1]
			return strings.Join(names, "-"), "Deployment", true
		default:
			return owner.Name, kind, true
		}
	}

	return "", "", false
}

func cpuTimeSeriesByRequest(podResource v1.ResourceList, scaleFactor float64) []float64 {
	timeSeries := make([]float64, portraitItemsLength, portraitItemsLength)

	if podResource.Cpu() != nil && !podResource.Cpu().IsZero() {
		cpuUsage := native.MultiplyResourceQuantity(v1.ResourceCPU, *podResource.Cpu(), scaleFactor)
		for i := range timeSeries {
			timeSeries[i] = float64(cpuUsage.MilliValue())
		}
	}
	return timeSeries
}

func memoryTimeSeriesByRequest(podResource v1.ResourceList, scaleFactor float64) []float64 {
	timeSeries := make([]float64, portraitItemsLength, portraitItemsLength)

	if podResource.Memory() != nil && !podResource.Memory().IsZero() {
		memoryUsage := native.MultiplyResourceQuantity(v1.ResourceMemory, *podResource.Memory(), scaleFactor)
		for i := range timeSeries {
			timeSeries[i] = float64(memoryUsage.Value())
		}
	}
	return timeSeries
}

func targetLoadPacking(targetRatio, usageRatio float64) (int64, error) {
	var score int64
	if targetRatio <= 0 || targetRatio >= 100 {
		return 0, fmt.Errorf("target %v is not supported", targetRatio)
	}
	if usageRatio < 0 {
		klog.Warningf("usageRatio %v less than zero", usageRatio)
		usageRatio = 0
	}
	if usageRatio > 100 {
		klog.Warningf("usageRatio %v greater than 100", usageRatio)
		return framework.MinNodeScore, nil
	}

	if usageRatio <= targetRatio {
		score = int64(math.Round((100-targetRatio)*usageRatio/targetRatio + targetRatio))
	} else {
		score = int64(math.Round(targetRatio * (100 - usageRatio) / (100 - targetRatio)))
	}

	return score, nil
}
