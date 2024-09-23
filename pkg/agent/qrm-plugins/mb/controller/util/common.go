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

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

func SumCCDMB(ccdMB map[int]int) int {
	sum := 0
	for _, mb := range ccdMB {
		sum += mb
	}
	return sum
}

func Sum(qosCCDMB map[task.QoSLevel]map[int]int) int {
	sum := 0

	for _, ccdMB := range qosCCDMB {
		sum += SumCCDMB(ccdMB)
	}
	return sum
}

func weightedSplit(total int, weights []int) []int {
	var sum float64
	for _, weight := range weights {
		sum += float64(weight)
	}

	results := make([]int, len(weights))
	for i, weight := range weights {
		results[i] = int(float64(total) / sum * float64(weight))
	}

	return results
}

func CoefficientWeightedSplit(total int, weights []int, coefficents []int) []int {
	comboWeights := make([]int, len(weights))
	for i, w := range weights {
		comboWeights[i] = w * coefficents[i]
	}

	return weightedSplit(total, comboWeights)
}

// GetMaxDedicatedToIncrease determines the MB room for dedicated qos
func GetMaxDedicatedToIncrease(ccdMB map[int]int) int {
	upperLimit := len(ccdMB) * mbdomain.MaxMBDedicatedPerNuma / 2
	return upperLimit - SumCCDMB(ccdMB)
}

func GetQoSKeys(qosMB map[task.QoSLevel]map[int]int) []task.QoSLevel {
	keys := make([]task.QoSLevel, len(qosMB))
	i := 0
	for qos, _ := range qosMB {
		keys[i] = qos
		i++
	}
	return keys
}
