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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestGetTopNPodUsages(t *testing.T) {
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
	resultMap := getTopNPodUsages(podRealUsage, 3)
	expected := []string{"default/test-1", "default/test-4", "default/test-7"}
	assert.Equal(t, len(resultMap), 3)
	for _, v := range expected {
		if _, ok := resultMap[v]; !ok {
			t.Error(fmt.Errorf("not exit"))
		}
	}
}
