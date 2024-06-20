package sorter

import corev1 "k8s.io/api/core/v1"

// PodUsage compares objs by the actual usage
func PodUsage(podRealUsage map[string]corev1.ResourceList, totalPodUsage corev1.ResourceList, resourceToWeightMap map[corev1.ResourceName]int64) CompareFn {
	scorer := ResourceUsageScorer(resourceToWeightMap)
	return func(p1, p2 *Obj) int {
		p1Usage, p1Found := podRealUsage[p1.Name]
		p2Usage, p2Found := podRealUsage[p2.Name]
		if !p1Found || !p2Found {
			return cmpBool(!p1Found, !p2Found)
		}
		p1Score := scorer(p1Usage, totalPodUsage)
		p2Score := scorer(p2Usage, totalPodUsage)
		if p1Score == p2Score {
			return 0
		}
		if p1Score > p2Score {
			return 1
		}
		return -1
	}
}

// SortPodsByUsage ...
func SortPodsByUsage(objs []*Obj, podRealUsage map[string]corev1.ResourceList, nodeAllocatableMap corev1.ResourceList, resourceToWeightMap map[corev1.ResourceName]int64) {
	OrderedBy(Reverse(PodUsage(podRealUsage, nodeAllocatableMap, resourceToWeightMap))).Sort(objs)
}
