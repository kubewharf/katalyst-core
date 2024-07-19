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
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
)

func TestIsLoadAwareEnabled(t *testing.T) {
	t.Parallel()

	p := &Plugin{
		args: &config.LoadAwareArgs{
			PodAnnotationLoadAwareEnable: new(string),
		},
	}
	*p.args.PodAnnotationLoadAwareEnable = ""

	testpod := &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Annotations: map[string]string{
				"loadAwareEnable": "false",
			},
		},
	}

	assert.True(t, p.IsLoadAwareEnabled(testpod))

	*p.args.PodAnnotationLoadAwareEnable = "loadAwareEnable"
	assert.False(t, p.IsLoadAwareEnabled(testpod))

	testpod.Annotations["loadAwareEnable"] = "true"
	assert.True(t, p.IsLoadAwareEnabled(testpod))
}

func TestPortraitByRequest(t *testing.T) {
	t.Parallel()

	p := Plugin{
		args: &config.LoadAwareArgs{
			PodAnnotationLoadAwareEnable: new(string),
		},
	}

	testpod := &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Name: "testPod",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "testContainer",
					Resources: v1.ResourceRequirements{
						Requests: map[v1.ResourceName]resource.Quantity{
							v1.ResourceCPU:    resource.MustParse("4"),
							v1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				},
			},
		},
	}

	resourceUsage := p.portraitByRequest(testpod)
	assert.Equal(t, len(resourceUsage.Cpu), portraitItemsLength)
	assert.Equal(t, len(resourceUsage.Memory), portraitItemsLength)

	assert.Equal(t, resourceUsage.Cpu[0], 4000.0)
	assert.Equal(t, resourceUsage.Memory[0], float64(8*1024*1024*1024))
}
