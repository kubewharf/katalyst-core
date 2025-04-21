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

package dynamicpolicy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestBuildCPUSetPodStateMap(t *testing.T) {
	t.Parallel()
	t.Run("test-empty-input", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{}
		result := p.buildCPUSetPodStateMap(context.Background(), state.PodEntries{}, make(map[string]map[string]machine.CPUSet))
		assert.Empty(t, result)
	})

	t.Run("test-with-pods", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{
			metaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &pod.PodFetcherStub{
						PodList: []*v1.Pod{
							{
								ObjectMeta: metav1.ObjectMeta{
									UID:       "test-pod",
									Namespace: "test-namespace",
									Name:      "test-pod",
								},
								Spec: v1.PodSpec{
									Containers: []v1.Container{
										{
											Name: "test-container",
											Resources: v1.ResourceRequirements{
												Requests: v1.ResourceList{
													v1.ResourceCPU: resource.MustParse("4"),
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		podUID := "test-pod"
		containerName := "test-container"
		cpuset := machine.MustParse("0-3")

		podEntries := state.PodEntries{
			podUID: {
				containerName: &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podUID,
						PodNamespace:  "test-namespace",
						PodName:       "test-pod",
						ContainerName: containerName,
						ContainerType: v1alpha1.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
					},
					AllocationResult: cpuset,
				},
			},
		}

		actualCPUSets := map[string]map[string]machine.CPUSet{
			podUID: {
				containerName: cpuset,
			},
		}

		result := p.buildCPUSetPodStateMap(context.Background(), podEntries, actualCPUSets)
		require.Len(t, result, 1)
		assert.Equal(t, cpuset, result["0-3"].cpuset)
	})
}

func TestCalculateTotalCPURequest(t *testing.T) {
	t.Parallel()
	t.Run("test-no-subset", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{}
		cs := &cpusetPodState{
			totalMilliCPURequest: 1000,
		}
		result := p.calculateTotalCPURequest("0-3", cs, make(map[string]*cpusetPodState))
		assert.Equal(t, int64(1000), result)
	})

	t.Run("test-with-subset", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{}
		cs := &cpusetPodState{
			cpuset:               machine.MustParse("0-3"),
			totalMilliCPURequest: 1000,
		}

		cpusetPodStateMap := map[string]*cpusetPodState{
			"0-1": {
				cpuset:               machine.MustParse("0-1"),
				totalMilliCPURequest: 500,
			},
			"2-3": {
				cpuset:               machine.MustParse("2-3"),
				totalMilliCPURequest: 500,
			},
		}

		result := p.calculateTotalCPURequest("0-3", cs, cpusetPodStateMap)
		assert.Equal(t, int64(2000), result)
	})
}

func TestEmitExceededMetrics(t *testing.T) {
	t.Parallel()
	t.Run("test-shared-cores", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{
			emitter:       &metrics.DummyMetrics{},
			dynamicConfig: dynamicconfig.NewDynamicAgentConfiguration(),
		}

		podUID := "test-pod"
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID:       "test-pod",
				Namespace: "test-namespace",
				Name:      "test-pod",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("2"),
							},
						},
					},
				},
			},
		}

		cs := &cpusetPodState{
			cpuset:               machine.MustParse("0-1"),
			totalMilliCPURequest: 3000,
			podMap: map[string]*v1.Pod{
				podUID: pod,
			},
		}

		podEntries := state.PodEntries{
			podUID: {
				"test-container": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podUID,
						ContainerName: "test-container",
						ContainerType: v1alpha1.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
		}

		p.emitExceededMetrics(podEntries, "0-1", cs, 0, false)
	})

	t.Run("test-dedicated-cores", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{
			emitter:       &metrics.DummyMetrics{},
			dynamicConfig: dynamicconfig.NewDynamicAgentConfiguration(),
		}

		podUID := "test-pod"
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID:       "test-pod",
				Namespace: "test-namespace",
				Name:      "test-pod",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("2"),
							},
						},
					},
				},
			},
		}

		cs := &cpusetPodState{
			cpuset:               machine.MustParse("0-1"),
			totalMilliCPURequest: 3000,
			podMap: map[string]*v1.Pod{
				podUID: pod,
			},
		}

		podEntries := state.PodEntries{
			podUID: {
				"test-container": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podUID,
						ContainerName: "test-container",
						ContainerType: v1alpha1.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
		}

		p.emitExceededMetrics(podEntries, "0-1", cs, 0, false)
	})
}
