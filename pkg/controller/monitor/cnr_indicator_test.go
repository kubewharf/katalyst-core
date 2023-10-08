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

package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func Test_checkNumaExclusiveAnomaly(t *testing.T) {
	t.Parallel()

	type fields struct {
		pods []*v1.Pod
		cnr  *v1alpha1.CustomNodeResource
	}

	tests := []struct {
		name       string
		fields     fields
		wantResult bool
	}{
		{
			name: "numa exclusive anomaly with numa binding and numa exclusive pod",
			fields: fields{
				pods: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod1",
							Namespace: "test-namespace",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: `{
									"numa_binding": "true", 
									"numa_exclusive": "true"
								  }`,
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod2",
							Namespace: "test-namespace",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: `{
									"numa_binding": "true", 
									"numa_exclusive": "true"
								  }`,
							},
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1alpha1.CustomNodeResourceStatus{
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: v1alpha1.TopologyTypeSocket,
								Children: []*v1alpha1.TopologyZone{
									{
										Type: v1alpha1.TopologyTypeNIC,
										Attributes: []v1alpha1.Attribute{
											{
												Name:  "katalyst.kubewharf.io/resource_identifier",
												Value: "enp0s3",
											},
										},
										Name: "eth0",
									},
									{
										Type: v1alpha1.TopologyTypeNuma,
										Allocations: []*v1alpha1.Allocation{
											{
												Consumer: "test-namespace/test-pod1/13414141",
											},
											{
												Consumer: "test-namespace/test-pod2/31413414",
											},
										},
										Name: "0",
									},
								},
							},
						},
					},
				},
			},
			wantResult: true,
		},
		{
			name: "numa exclusive anomaly test with non numa binding and non numa exclusive pod",
			fields: fields{
				pods: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod1",
							Namespace: "test-namespace",
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod2",
							Namespace: "test-namespace",
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1alpha1.CustomNodeResourceStatus{
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: v1alpha1.TopologyTypeSocket,
								Children: []*v1alpha1.TopologyZone{
									{
										Type: v1alpha1.TopologyTypeNIC,
										Attributes: []v1alpha1.Attribute{
											{
												Name:  "katalyst.kubewharf.io/resource_identifier",
												Value: "enp0s3",
											},
										},
										Name: "eth0",
									},
									{
										Type: v1alpha1.TopologyTypeNuma,
										Allocations: []*v1alpha1.Allocation{
											{
												Consumer: "test-namespace/test-pod1/13414141",
											},
											{
												Consumer: "test-namespace/test-pod2/31413414",
											},
										},
										Name: "0",
									},
								},
							},
						},
					},
				},
			},
			wantResult: false,
		},
		{
			name: "numa exclusive anomaly with non numa binding and numa exclusive pod",
			fields: fields{
				pods: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod1",
							Namespace: "test-namespace",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: `{
									"numa_binding": "false", 
									"numa_exclusive": "true"
								  }`,
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod2",
							Namespace: "test-namespace",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: `{
									"numa_binding": "true", 
									"numa_exclusive": "false"
								  }`,
							},
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1alpha1.CustomNodeResourceStatus{
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: v1alpha1.TopologyTypeSocket,
								Children: []*v1alpha1.TopologyZone{
									{
										Type: v1alpha1.TopologyTypeNIC,
										Attributes: []v1alpha1.Attribute{
											{
												Name:  "katalyst.kubewharf.io/resource_identifier",
												Value: "enp0s3",
											},
										},
										Name: "eth0",
									},
									{
										Type: v1alpha1.TopologyTypeNuma,
										Allocations: []*v1alpha1.Allocation{
											{
												Consumer: "test-namespace/test-pod1/13414141",
											},
											{
												Consumer: "test-namespace/test-pod2/31413414",
											},
										},
										Name: "0",
									},
								},
							},
						},
					},
				},
			},
			wantResult: false,
		},
		{
			name: "numa exclusive anomaly with numa binding and non numa exclusive pod",
			fields: fields{
				pods: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod1",
							Namespace: "test-namespace",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: `{
									"numa_binding": "true", 
									"numa_exclusive": "false"
								  }`,
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod2",
							Namespace: "test-namespace",
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1alpha1.CustomNodeResourceStatus{
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: v1alpha1.TopologyTypeSocket,
								Children: []*v1alpha1.TopologyZone{
									{
										Type: v1alpha1.TopologyTypeNIC,
										Attributes: []v1alpha1.Attribute{
											{
												Name:  "katalyst.kubewharf.io/resource_identifier",
												Value: "enp0s3",
											},
										},
										Name: "eth0",
									},
									{
										Type: v1alpha1.TopologyTypeNuma,
										Allocations: []*v1alpha1.Allocation{
											{
												Consumer: "test-namespace/test-pod1/13414141",
											},
											{
												Consumer: "test-namespace/test-pod2/31413414",
											},
										},
										Name: "0",
									},
								},
							},
						},
					},
				},
			},
			wantResult: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{tt.fields.pods[0], tt.fields.pods[1]}, []runtime.Object{tt.fields.cnr})
			assert.NoError(t, err)

			conf, err := options.NewOptions().Config()
			require.NoError(t, err)
			require.NotNil(t, conf)

			ctrl, err := NewCNRMonitorController(
				context.Background(),
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.CNRMonitorConfig,
				genericCtx.Client,
				genericCtx.KubeInformerFactory.Core().V1().Nodes(),
				genericCtx.KubeInformerFactory.Core().V1().Pods(),
				genericCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources(),
				metricspool.DummyMetricsEmitterPool.GetDefaultMetricsEmitter(metricspool.DummyMetricsEmitterPool{}),
			)
			assert.NoError(t, err)

			// test cache synced
			genericCtx.KubeInformerFactory.Start(ctrl.ctx.Done())
			genericCtx.InternalInformerFactory.Start(ctrl.ctx.Done())

			cache.WaitForCacheSync(ctrl.ctx.Done(), ctrl.cnrListerSynced, ctrl.podListerSynced)
			time.Sleep(100 * time.Millisecond)

			result := ctrl.checkNumaExclusiveAnomaly(tt.fields.cnr)
			assert.Equal(t, tt.wantResult, result)
		})
	}
}

func Test_checkNumaAllocatableSumAnomaly(t *testing.T) {
	t.Parallel()

	type fields struct {
		node *v1.Node
		cnr  *v1alpha1.CustomNodeResource
	}

	tests := []struct {
		name       string
		fields     fields
		wantResult bool
	}{
		{
			name: "numa allocatable sum anomaly not found",
			fields: fields{
				node: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1.NodeStatus{
						Allocatable: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewQuantity(int64(6), resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(int64(2000), resource.BinarySI),
						},
						Capacity: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewQuantity(int64(6), resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(int64(2000), resource.BinarySI),
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1alpha1.CustomNodeResourceStatus{
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: v1alpha1.TopologyTypeSocket,
								Children: []*v1alpha1.TopologyZone{
									{
										Type: v1alpha1.TopologyTypeNIC,
										Attributes: []v1alpha1.Attribute{
											{
												Name:  "katalyst.kubewharf.io/resource_identifier",
												Value: "enp0s3",
											},
										},
										Name: "eth0",
									},
									{
										Type: v1alpha1.TopologyTypeNuma,
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												v1.ResourceCPU:    *resource.NewQuantity(int64(3), resource.DecimalSI),
												v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
											},
										},
										Name: "0",
									},
									{
										Type: v1alpha1.TopologyTypeNuma,
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												v1.ResourceCPU:    *resource.NewQuantity(int64(3), resource.DecimalSI),
												v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
											},
										},
										Name: "1",
									},
								},
							},
						},
					},
				},
			},
			wantResult: false,
		},
		{
			name: "numa allocatable sum anomaly found",
			fields: fields{
				node: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1.NodeStatus{
						Allocatable: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewQuantity(int64(5), resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
						},
						Capacity: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewQuantity(int64(5), resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1alpha1.CustomNodeResourceStatus{
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: v1alpha1.TopologyTypeSocket,
								Children: []*v1alpha1.TopologyZone{
									{
										Type: v1alpha1.TopologyTypeNIC,
										Attributes: []v1alpha1.Attribute{
											{
												Name:  "katalyst.kubewharf.io/resource_identifier",
												Value: "enp0s3",
											},
										},
										Name: "eth0",
									},
									{
										Type: v1alpha1.TopologyTypeNuma,
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												v1.ResourceCPU:    *resource.NewQuantity(int64(3), resource.DecimalSI),
												v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
											},
										},
										Name: "0",
									},
								},
							},
							{
								Type: v1alpha1.TopologyTypeSocket,
								Children: []*v1alpha1.TopologyZone{
									{
										Type: v1alpha1.TopologyTypeNuma,
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												v1.ResourceCPU:    *resource.NewQuantity(int64(3), resource.DecimalSI),
												v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
											},
										},
										Name: "0",
									},
								},
							},
						},
					},
				},
			},
			wantResult: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{tt.fields.node}, []runtime.Object{tt.fields.cnr})
			assert.NoError(t, err)

			conf, err := options.NewOptions().Config()
			require.NoError(t, err)
			require.NotNil(t, conf)

			ctrl, err := NewCNRMonitorController(
				context.Background(),
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.CNRMonitorConfig,
				genericCtx.Client,
				genericCtx.KubeInformerFactory.Core().V1().Nodes(),
				genericCtx.KubeInformerFactory.Core().V1().Pods(),
				genericCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources(),
				metricspool.DummyMetricsEmitterPool.GetDefaultMetricsEmitter(metricspool.DummyMetricsEmitterPool{}),
			)
			assert.NoError(t, err)

			// test cache synced
			genericCtx.KubeInformerFactory.Start(ctrl.ctx.Done())
			genericCtx.InternalInformerFactory.Start(ctrl.ctx.Done())

			cache.WaitForCacheSync(ctrl.ctx.Done(), ctrl.cnrListerSynced, ctrl.podListerSynced)
			time.Sleep(100 * time.Millisecond)

			result := ctrl.checkNumaAllocatableSumAnomaly(tt.fields.cnr)
			assert.Equal(t, tt.wantResult, result)
		})
	}
}

func Test_checkPodAllocationSumAnomaly(t *testing.T) {
	t.Parallel()

	type fields struct {
		pods []*v1.Pod
		cnr  *v1alpha1.CustomNodeResource
	}

	tests := []struct {
		name       string
		fields     fields
		wantResult bool
	}{
		{
			name: "pod allocatable sum anomaly not found",
			fields: fields{
				pods: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod1",
							Namespace: "test-namespace",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: `{
									"numa_binding": "true", 
									"numa_exclusive": "false"
								}`,
							},
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1alpha1.CustomNodeResourceStatus{
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: v1alpha1.TopologyTypeSocket,
								Children: []*v1alpha1.TopologyZone{
									{
										Type: v1alpha1.TopologyTypeNIC,
										Attributes: []v1alpha1.Attribute{
											{
												Name:  "katalyst.kubewharf.io/resource_identifier",
												Value: "enp0s3",
											},
										},
										Name: "eth0",
									},
									{
										Type: v1alpha1.TopologyTypeNuma,
										Allocations: []*v1alpha1.Allocation{
											{
												Consumer: "test-namespace/test-pod1/13414141",
												Requests: &v1.ResourceList{
													v1.ResourceCPU:    *resource.NewQuantity(int64(3), resource.DecimalSI),
													v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
												},
											},
										},
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												v1.ResourceCPU:    *resource.NewQuantity(int64(3), resource.DecimalSI),
												v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
											},
										},
										Name: "0",
									},
								},
							},
						},
					},
				},
			},
			wantResult: false,
		},
		{
			name: "pod allocatable sum anomaly found",
			fields: fields{
				pods: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod1",
							Namespace: "test-namespace",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: `{
									"numa_binding": "true", 
									"numa_exclusive": "false"
								}`,
							},
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Status: v1alpha1.CustomNodeResourceStatus{
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: v1alpha1.TopologyTypeSocket,
								Children: []*v1alpha1.TopologyZone{
									{
										Type: v1alpha1.TopologyTypeNIC,
										Attributes: []v1alpha1.Attribute{
											{
												Name:  "katalyst.kubewharf.io/resource_identifier",
												Value: "enp0s3",
											},
										},
										Name: "eth0",
									},
									{
										Type: v1alpha1.TopologyTypeNuma,
										Allocations: []*v1alpha1.Allocation{
											{
												Consumer: "test-namespace/test-pod1/13414141",
												Requests: &v1.ResourceList{
													v1.ResourceCPU:    *resource.NewQuantity(int64(4), resource.DecimalSI),
													v1.ResourceMemory: *resource.NewQuantity(int64(1001), resource.BinarySI),
												},
											},
										},
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												v1.ResourceCPU:    *resource.NewQuantity(int64(3), resource.DecimalSI),
												v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
											},
										},
										Name: "0",
									},
								},
							},
						},
					},
				},
			},
			wantResult: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{tt.fields.pods[0]}, []runtime.Object{tt.fields.cnr})
			assert.NoError(t, err)

			conf, err := options.NewOptions().Config()
			require.NoError(t, err)
			require.NotNil(t, conf)

			ctrl, err := NewCNRMonitorController(
				context.Background(),
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.CNRMonitorConfig,
				genericCtx.Client,
				genericCtx.KubeInformerFactory.Core().V1().Nodes(),
				genericCtx.KubeInformerFactory.Core().V1().Pods(),
				genericCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources(),
				metricspool.DummyMetricsEmitterPool.GetDefaultMetricsEmitter(metricspool.DummyMetricsEmitterPool{}),
			)
			assert.NoError(t, err)

			// test cache synced
			genericCtx.KubeInformerFactory.Start(ctrl.ctx.Done())
			genericCtx.InternalInformerFactory.Start(ctrl.ctx.Done())

			cache.WaitForCacheSync(ctrl.ctx.Done(), ctrl.cnrListerSynced, ctrl.podListerSynced)
			time.Sleep(100 * time.Millisecond)

			result := ctrl.checkPodAllocationSumAnomaly(tt.fields.cnr)
			assert.Equal(t, tt.wantResult, result)
		})
	}
}

func Test_checkAndEmitCNRReportLantencyMetric(t *testing.T) {
	t.Parallel()

	cnr := &v1alpha1.CustomNodeResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Status: v1alpha1.CustomNodeResourceStatus{
			TopologyZone: []*v1alpha1.TopologyZone{
				{
					Type: v1alpha1.TopologyTypeSocket,
					Children: []*v1alpha1.TopologyZone{
						{
							Type: v1alpha1.TopologyTypeNIC,
							Attributes: []v1alpha1.Attribute{
								{
									Name:  "katalyst.kubewharf.io/resource_identifier",
									Value: "enp0s3",
								},
							},
							Name: "eth0",
						},
						{
							Type: v1alpha1.TopologyTypeNuma,
							Allocations: []*v1alpha1.Allocation{
								{
									Consumer: "test-namespace/test-pod1/1111111111",
									Requests: &v1.ResourceList{
										v1.ResourceCPU:    *resource.NewQuantity(int64(3), resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
									},
								},
							},
							Resources: v1alpha1.Resources{
								Allocatable: &v1.ResourceList{
									v1.ResourceCPU:    *resource.NewQuantity(int64(3), resource.DecimalSI),
									v1.ResourceMemory: *resource.NewQuantity(int64(1000), resource.BinarySI),
								},
							},
							Name: "0",
						},
					},
				},
			},
		},
	}

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{}, []runtime.Object{cnr})
	assert.NoError(t, err)

	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	ctrl, err := NewCNRMonitorController(
		context.Background(),
		conf.GenericConfiguration,
		conf.GenericControllerConfiguration,
		conf.CNRMonitorConfig,
		genericCtx.Client,
		genericCtx.KubeInformerFactory.Core().V1().Nodes(),
		genericCtx.KubeInformerFactory.Core().V1().Pods(),
		genericCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources(),
		metricspool.DummyMetricsEmitterPool.GetDefaultMetricsEmitter(metricspool.DummyMetricsEmitterPool{}),
	)

	ctrl.podTimeMap.Store("test-namespace/test-pod1/1111111111", time.Now())
	time.Sleep(2 * time.Millisecond)
	assert.NoError(t, err)

	err = ctrl.checkAndEmitCNRReportLantencyMetric(cnr)
	assert.NoError(t, err)
	if _, ok := ctrl.podTimeMap.Load("test-namespace/test-pod1/1111111111"); ok {
		t.Errorf("podTimeMap should not have key test-namespace/test-pod1/1111111111")
	}
}
