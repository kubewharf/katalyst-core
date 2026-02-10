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
	"fmt"
	"math"
	"strconv"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func AnnotationsIndicateNUMANotShare(annotations map[string]string) bool {
	return annotations[consts.PodAnnotationCPUEnhancementNUMAShare] ==
		consts.PodAnnotationCPUEnhancementNUMAShareDisable
}

// GetPodCPUSuppressionToleranceRate parses cpu suppression tolerance rate for the given pod,
// and cpu suppression is only supported for reclaim pods. if the given is not nominated with
// cpu suppression, return max to indicate that it can be suppressed for any degree.
func GetPodCPUSuppressionToleranceRate(qosConf *generic.QoSConfiguration, pod *v1.Pod) (float64, error) {
	qosLevel, _ := qosConf.GetQoSLevel(pod, map[string]string{})
	if qosLevel != consts.PodAnnotationQoSLevelReclaimedCores {
		return 0, fmt.Errorf("qos level %s not support cpu suppression", qosLevel)
	}

	cpuEnhancement := qosConf.GetQoSEnhancementKVs(pod, map[string]string{}, consts.PodAnnotationCPUEnhancementKey)
	suppressionToleranceRateStr, ok := cpuEnhancement[consts.PodAnnotationCPUEnhancementSuppressionToleranceRate]
	if ok {
		suppressionToleranceRate, err := strconv.ParseFloat(suppressionToleranceRateStr, 64)
		if err != nil {
			return 0, err
		}
		return suppressionToleranceRate, nil
	}

	return math.MaxFloat64, nil
}

// GetPodCPUBurstPolicyFromCPUEnhancement gets the cpu burst policy for the given pod by parsing the cpu enhancement keys.
// All reclaimed cores pods should not have cpu burst enabled.
func GetPodCPUBurstPolicyFromCPUEnhancement(qosConf *generic.QoSConfiguration, pod *v1.Pod) string {
	qosLevel, _ := qosConf.GetQoSLevel(pod, map[string]string{})
	cpuEnhancement := qosConf.GetQoSEnhancementKVs(pod, map[string]string{}, consts.PodAnnotationCPUEnhancementKey)
	cpuBurstPolicy, ok := cpuEnhancement[consts.PodAnnotationCPUEnhancementCPUBurstPolicy]

	// Do not enable cpu burst for reclaimed cores pods even when the annotation is set
	if qosLevel == consts.PodAnnotationQoSLevelReclaimedCores && ok && cpuBurstPolicy != consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed {
		general.Warningf("Reclaimed cores should not have cpu burst enabled")
		return consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed
	}

	if !ok {
		return consts.PodAnnotationCPUEnhancementCPUBurstPolicyDefault
	}

	return cpuBurstPolicy
}

// GetPodCPUBurstPercentFromCPUEnhancement parses cpu burst percent for the given pod by parsing the cpu enhancement keys.
func GetPodCPUBurstPercentFromCPUEnhancement(qosConf *generic.QoSConfiguration, pod *v1.Pod) (float64, bool, error) {
	cpuEnhancement := qosConf.GetQoSEnhancementKVs(pod, map[string]string{}, consts.PodAnnotationCPUEnhancementKey)
	cpuBurstPercentStr, ok := cpuEnhancement[consts.PodAnnotationCPUEnhancementCPUBurstPercent]

	if !ok {
		return 0, false, nil
	}

	cpuBurstPercent, err := strconv.ParseFloat(cpuBurstPercentStr, 64)
	if err != nil {
		return 0, false, fmt.Errorf("failed to parse cpuBurstPercent: %v", err)
	}

	// cpu burst percent should be in range [0, 100]
	if cpuBurstPercent > 100 {
		return 100, true, nil
	}

	return cpuBurstPercent, true, nil
}

// AnnotationsIndicateAlignBySocket returns true if the given annotations indicate that the pod should be aligned by socket.
func AnnotationsIndicateAlignBySocket(annotations map[string]string) bool {
	if len(annotations) == 0 {
		return false
	}

	return annotations[consts.PodAnnotationCPUEnhancementAlignBySocket] == consts.PodAnnotationCPUEnhancementAlignBySocketEnable
}

// AnnotationsIndicateDistributeEvenlyAcrossNuma returns true if the given annotations indicate that the pod's cpu request
// should be evenly distributed across NUMA nodes.
func AnnotationsIndicateDistributeEvenlyAcrossNuma(annotations map[string]string) bool {
	if len(annotations) == 0 {
		return false
	}

	return annotations[consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma] == consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable
}

// AnnotationsIndicateFullPCPUsPairing returns true if the given annotations indicate that the pod should be allocated to
// full physical cores only.
func AnnotationsIndicateFullPCPUsPairing(annotations map[string]string) bool {
	if len(annotations) == 0 {
		return false
	}

	return annotations[consts.PodAnnotationCPUEnhancementFullPCPUsPairing] == consts.PodAnnotationCPUEnhancementFullPCPUsPairingEnable
}

// AnnotationsGetNUMANumber gets the number of NUMA nodes that the pod should be allocated to.
func AnnotationsGetNUMANumber(annotations map[string]string, maxNumaNumber int, key string) (int, error) {
	numaNumberStr, ok := annotations[key]
	if !ok || numaNumberStr == "" {
		return 0, nil
	}

	numaNumber, err := strconv.Atoi(numaNumberStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse numa number: %w", err)
	}

	if numaNumber < 0 || numaNumber > maxNumaNumber {
		return 0, fmt.Errorf("numa number %d out of range, lowest 0 and highest: %d", numaNumber, maxNumaNumber)
	}

	return numaNumber, nil
}

// AnnotationsGetNUMAIDs gets the specific NUMA IDs that the pod should be allocated to in the form of a bitmask.
// It takes in the machine NUMA nodes as a parameter and makes sure that the NUMA IDs are valid.
func AnnotationsGetNUMAIDs(annotations map[string]string, numaNodes []int, key string) (machine.BitMask, error) {
	numaIDsStr, ok := annotations[key]
	if !ok || numaIDsStr == "" {
		return machine.NewEmptyBitMask(), nil
	}

	numaSet, err := machine.Parse(numaIDsStr)
	if err != nil {
		return machine.NewEmptyBitMask(), fmt.Errorf("failed to parse numa ids: %w", err)
	}

	// Verify that the numa IDs are a subset of the numa nodes in the machine
	machineNumaSet := machine.NewCPUSet(numaNodes...)
	if !numaSet.IsSubsetOf(machineNumaSet) {
		return machine.NewEmptyBitMask(), fmt.Errorf("invalid numa ids: %v as they are not a subset of machine numa nodes: %v", numaSet, machineNumaSet)
	}

	mask, err := machine.NewBitMask(numaSet.ToSliceInt()...)
	if err != nil {
		return machine.NewEmptyBitMask(), fmt.Errorf("failed to convert numa ids to bitmask: %w", err)
	}

	return mask, nil
}
