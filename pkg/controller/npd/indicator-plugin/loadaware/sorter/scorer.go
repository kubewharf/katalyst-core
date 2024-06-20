package sorter

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ResourceUsageScorer ...
func ResourceUsageScorer(resToWeightMap map[corev1.ResourceName]int64) func(requested, allocatable corev1.ResourceList) int64 {
	return func(requested, allocatable corev1.ResourceList) int64 {
		var nodeScore, weightSum int64
		for resourceName, quantity := range requested {
			weight := resToWeightMap[resourceName]
			resourceScore := mostRequestedScore(getResourceValue(resourceName, quantity), getResourceValue(resourceName, allocatable[resourceName]))
			nodeScore += resourceScore * weight
			weightSum += weight
		}
		if weightSum == 0 {
			return 0
		}
		return nodeScore / weightSum
	}
}

func mostRequestedScore(requested, capacity int64) int64 {
	if capacity == 0 {
		return 0
	}
	if requested > capacity {
		// `requested` might be greater than `capacity` because objs with no
		// requests get minimum values.
		requested = capacity
	}

	return (requested * 1000) / capacity
}

func getResourceValue(resourceName corev1.ResourceName, quantity resource.Quantity) int64 {
	if resourceName == corev1.ResourceCPU {
		return quantity.MilliValue()
	}
	return quantity.Value()
}
