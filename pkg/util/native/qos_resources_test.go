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

package native

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
)

var makeQoSResourcePod = func(name string, container, initContainer, overhead v1.ResourceList) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Limits:   container,
						Requests: container,
					},
				},
				{
					Name: "c2",
					Resources: v1.ResourceRequirements{
						Limits:   container,
						Requests: container,
					},
				},
			},
			InitContainers: []v1.Container{
				{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Limits:   initContainer,
						Requests: initContainer,
					},
				},
			},
			Overhead: overhead,
		},
	}
	return pod
}

func Test_CalculateQoSResource(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		pod  *v1.Pod

		res     QoSResource
		non0CPU int64
		non0Mem int64
	}{
		{
			name: "test with resource set",
			pod: makeQoSResourcePod("pod-1", map[v1.ResourceName]resource.Quantity{
				consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(2000, resource.DecimalSI),
				consts.ReclaimedResourceMemory:   *resource.NewQuantity(2*1024*0o124*1024, resource.DecimalSI),
			}, map[v1.ResourceName]resource.Quantity{
				consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(3000, resource.DecimalSI),
				consts.ReclaimedResourceMemory:   *resource.NewQuantity(3*1024*0o124*1024, resource.DecimalSI),
			}, map[v1.ResourceName]resource.Quantity{
				consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(1000, resource.DecimalSI),
				consts.ReclaimedResourceMemory:   *resource.NewQuantity(1*1024*0o124*1024, resource.DecimalSI),
			}),
			res:     QoSResource{5000, 5 * 1024 * 0o124 * 1024},
			non0CPU: 5000,
			non0Mem: 5 * 1024 * 0o124 * 1024,
		},
		{
			name: "test with resource init",
			pod: makeQoSResourcePod("pod-1", map[v1.ResourceName]resource.Quantity{
				consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(2000, resource.DecimalSI),
				consts.ReclaimedResourceMemory:   *resource.NewQuantity(2*1024*0o124*1024, resource.DecimalSI),
			}, map[v1.ResourceName]resource.Quantity{
				consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(5000, resource.DecimalSI),
				consts.ReclaimedResourceMemory:   *resource.NewQuantity(5*1024*0o124*1024, resource.DecimalSI),
			}, map[v1.ResourceName]resource.Quantity{
				consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(1000, resource.DecimalSI),
				consts.ReclaimedResourceMemory:   *resource.NewQuantity(1*1024*0o124*1024, resource.DecimalSI),
			}),
			res:     QoSResource{6000, 6 * 1024 * 0o124 * 1024},
			non0CPU: 6000,
			non0Mem: 6 * 1024 * 0o124 * 1024,
		},
		{
			name: "test with resource missing",
			pod: makeQoSResourcePod("pod-1", map[v1.ResourceName]resource.Quantity{
				consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(2000, resource.DecimalSI),
			}, map[v1.ResourceName]resource.Quantity{
				consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(3000, resource.DecimalSI),
			}, map[v1.ResourceName]resource.Quantity{
				consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(1000, resource.DecimalSI),
				consts.ReclaimedResourceMemory:   *resource.NewQuantity(1*1024*0o124*1024, resource.DecimalSI),
			}),
			res:     QoSResource{5000, 1 * 1024 * 0o124 * 1024},
			non0CPU: 5000,
			non0Mem: 1*1024*0o124*1024 + DefaultReclaimedMemoryRequest*2,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("case: %v", tc.name)
			res, non0CPU, non0Mem := CalculateQoSResource(tc.pod)
			assert.Equal(t, tc.res, res)
			assert.Equal(t, tc.non0CPU, non0CPU)
			assert.Equal(t, tc.non0Mem, non0Mem)
		})
	}
}
