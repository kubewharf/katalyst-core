package sorter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	quotav1 "k8s.io/apiserver/pkg/quota/v1"
)

func TestSortPods(t *testing.T) {
	t.Parallel()
	podRealUsage := map[string]corev1.ResourceList{
		"default/test-1": {
			corev1.ResourceCPU:    resource.MustParse("80"),
			corev1.ResourceMemory: resource.MustParse("10Gi"),
		},
		"default/test-2": {
			corev1.ResourceCPU:    resource.MustParse("30"),
			corev1.ResourceMemory: resource.MustParse("10Gi"),
		},
		"default/test-3": {
			corev1.ResourceCPU:    resource.MustParse("50"),
			corev1.ResourceMemory: resource.MustParse("10Gi"),
		},
		"default/test-4": {
			corev1.ResourceCPU:    resource.MustParse("70"),
			corev1.ResourceMemory: resource.MustParse("10Gi"),
		},
		"default/test-5": {
			corev1.ResourceCPU:    resource.MustParse("10"),
			corev1.ResourceMemory: resource.MustParse("10Gi"),
		},
		"default/test-6": {
			corev1.ResourceCPU:    resource.MustParse("40"),
			corev1.ResourceMemory: resource.MustParse("10Gi"),
		},
		"default/test-7": {
			corev1.ResourceCPU:    resource.MustParse("60"),
			corev1.ResourceMemory: resource.MustParse("10Gi"),
		},
	}

	resourceToWeightMap := map[corev1.ResourceName]int64{
		corev1.ResourceCPU:    1,
		corev1.ResourceMemory: 1,
	}
	var objs []*Obj
	totalResUsage := make(corev1.ResourceList)
	for name, usage := range podRealUsage {
		obj := Obj{
			Name: name,
		}
		objs = append(objs, &obj)
		totalResUsage = quotav1.Add(totalResUsage, usage)
	}
	SortPodsByUsage(objs, podRealUsage, totalResUsage, resourceToWeightMap)
	expectedPodsOrder := []string{"default/test-1", "default/test-4", "default/test-7", "default/test-3", "default/test-6", "default/test-2", "default/test-5"}
	var podsOrder []string
	for _, v := range objs {
		podsOrder = append(podsOrder, v.Name)
	}
	assert.Equal(t, expectedPodsOrder, podsOrder)
}
