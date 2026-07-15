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

package cpuweight

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/finegrainedresource"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
)

func generateTestMetaServer(pods []*v1.Pod) *metaserver.MetaServer {
	return generateTestMetaServerWithPodFetcher(&pod.PodFetcherStub{PodList: pods})
}

func generateTestMetaServerWithPodFetcher(podFetcher pod.PodFetcher) *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: podFetcher,
		},
	}
}

type errPodFetcher struct {
	*pod.PodFetcherStub
	err error
}

func (e *errPodFetcher) GetPodList(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	if e.err != nil {
		return nil, e.err
	}

	return e.PodFetcherStub.GetPodList(context.Background(), nil)
}

type metricRecord struct {
	name string
	val  int64
	tags map[string]string
}

type recordingEmitter struct {
	records []metricRecord
}

func (r *recordingEmitter) StoreInt64(name string, val int64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	record := metricRecord{name: name, val: val, tags: map[string]string{}}
	for _, tag := range tags {
		record.tags[tag.Key] = tag.Val
	}
	r.records = append(r.records, record)
	return nil
}

func (r *recordingEmitter) StoreFloat64(_ string, _ float64, _ metrics.MetricTypeName, _ ...metrics.MetricTag) error {
	return nil
}

func (r *recordingEmitter) WithTags(_ string, _ ...metrics.MetricTag) metrics.MetricEmitter {
	return r
}

func (r *recordingEmitter) Run(_ context.Context) {}

func assertMetricRecord(t *testing.T, records []metricRecord, name string, value int64, tags map[string]string) {
	t.Helper()
	for _, record := range records {
		if record.name == name && record.val == value && assert.ObjectsAreEqual(tags, record.tags) {
			return
		}
	}
	assert.Failf(t, "metric not found", "missing metric %s=%d with tags %+v in records %+v", name, value, tags, records)
}

func assertNoMetricRecord(t *testing.T, records []metricRecord, name string, tags map[string]string) {
	t.Helper()
	for _, record := range records {
		if record.name == name && assert.ObjectsAreEqual(tags, record.tags) {
			assert.Failf(t, "unexpected metric found", "unexpected metric %s with tags %+v in records %+v", name, tags, records)
			return
		}
	}
}

func TestManager_GetManager(t *testing.T) {
	t.Parallel()

	instance = nil
	once = sync.Once{}
	t.Cleanup(func() {
		instance = nil
		once = sync.Once{}
	})

	metaServer1 := generateTestMetaServer(nil)
	metaServer2 := generateTestMetaServer(nil)

	manager1 := GetManager(metaServer1, nil)
	manager2 := GetManager(metaServer2, nil)

	assert.Same(t, manager1, manager2)
	assert.Same(t, metaServer1, manager1.(*managerImpl).metaServer)
}

func TestManager_newManager(t *testing.T) {
	t.Parallel()

	podFetcher := &pod.PodFetcherStub{
		PodList: nil,
	}

	metaServer := generateTestMetaServerWithPodFetcher(podFetcher)
	manager := newManager(metaServer, nil)

	assert.NotNil(t, manager)
	assert.Same(t, metaServer, manager.metaServer)
}

func generateTestDynamicConfig(cpuWeightConfig *finegrainedresource.CPUWeightConfiguration) *dynamic.DynamicAgentConfiguration {
	conf := dynamic.NewConfiguration()
	conf.AdminQoSConfiguration.FineGrainedResourceConfiguration.CPUWeightConfiguration = cpuWeightConfig

	dynamicConfig := dynamic.NewDynamicAgentConfiguration()
	dynamicConfig.SetDynamicConfiguration(conf)
	return dynamicConfig
}

func generateTestPod(uid, name string, labels map[string]string, containers []v1.Container) *v1.Pod {
	containerStatuses := make([]v1.ContainerStatus, 0, len(containers))
	for _, c := range containers {
		containerStatuses = append(containerStatuses, v1.ContainerStatus{
			Name:        c.Name,
			ContainerID: "containerd://" + c.Name,
		})
	}

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:    types.UID(uid),
			Name:   name,
			Labels: labels,
		},
		Spec: v1.PodSpec{
			Containers: containers,
		},
		Status: v1.PodStatus{
			Phase:             v1.PodRunning,
			ContainerStatuses: containerStatuses,
		},
	}
}

func TestManager_UpdateCPUWeight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		metaServer        *metaserver.MetaServer
		config            *dynamic.DynamicAgentConfiguration
		expectShares      []uint64
		expectErrContains []string
		mockPathErr       error
		mockApplyErr      error
		mockApplyErrs     []error
		expectMetrics     []metricRecord
		expectNoMetrics   []metricRecord
	}{
		{
			name:         "nil cpu weight config",
			metaServer:   generateTestMetaServer(nil),
			config:       generateTestDynamicConfig(nil),
			expectShares: nil,
		},
		{
			name:       "nil dynamic config",
			metaServer: generateTestMetaServer(nil),
			config:     nil,
		},
		{
			name:       "nil admin qos configuration",
			metaServer: generateTestMetaServer(nil),
			config:     &dynamic.DynamicAgentConfiguration{},
		},
		{
			name:       "empty selectors are aggregated",
			metaServer: generateTestMetaServer(nil),
			config: generateTestDynamicConfig(&finegrainedresource.CPUWeightConfiguration{
				RestoreRules: []finegrainedresource.CPUWeightRestore{
					{Name: "restore-rule"},
				},
				OverrideRules: []finegrainedresource.CPUWeightOverride{
					{Name: "override-rule", PodCPUDemand: 4},
				},
			}),
			expectErrContains: []string{
				"failed to process restore rule restore-rule: empty pod selector",
				"failed to process override rule override-rule: empty pod selector",
			},
		},
		{
			name:       "invalid selectors are aggregated",
			metaServer: generateTestMetaServer(nil),
			config: generateTestDynamicConfig(&finegrainedresource.CPUWeightConfiguration{
				RestoreRules: []finegrainedresource.CPUWeightRestore{
					{Name: "restore-rule", PodSelector: "["},
				},
				OverrideRules: []finegrainedresource.CPUWeightOverride{
					{Name: "override-rule", PodSelector: "[", PodCPUDemand: 4},
				},
			}),
			expectErrContains: []string{
				"failed to process restore rule restore-rule: failed to parse pod selector",
				"failed to process override rule override-rule: failed to parse pod selector",
			},
		},
		{
			name: "restore get pods error",
			metaServer: generateTestMetaServerWithPodFetcher(&errPodFetcher{
				PodFetcherStub: &pod.PodFetcherStub{},
				err:            fmt.Errorf("get pods failed"),
			}),
			config: generateTestDynamicConfig(&finegrainedresource.CPUWeightConfiguration{
				RestoreRules: []finegrainedresource.CPUWeightRestore{
					{Name: "restore-rule", PodSelector: "app=restore-app"},
				},
			}),
			expectErrContains: []string{
				"failed to process restore rule restore-rule: failed to get pods: get pods failed",
			},
		},
		{
			name: "restore rule",
			metaServer: generateTestMetaServer([]*v1.Pod{
				generateTestPod("restore-pod", "restore-pod", map[string]string{"app": "restore-app"}, []v1.Container{
					{
						Name: "c1",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("2"),
							},
						},
					},
					{
						Name: "c2",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("4"),
							},
						},
					},
				}),
			}),
			config: generateTestDynamicConfig(&finegrainedresource.CPUWeightConfiguration{
				RestoreRules: []finegrainedresource.CPUWeightRestore{
					{
						Name:        "restore-rule",
						PodSelector: "app=restore-app",
					},
				},
			}),
			expectShares: []uint64{6144},
		},
		{
			name: "restore rule cgroup path error",
			metaServer: generateTestMetaServer([]*v1.Pod{
				generateTestPod("restore-pod", "restore-pod", map[string]string{"app": "restore-app"}, []v1.Container{
					{
						Name: "c1",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("2"),
							},
						},
					},
				}),
			}),
			config: generateTestDynamicConfig(&finegrainedresource.CPUWeightConfiguration{
				RestoreRules: []finegrainedresource.CPUWeightRestore{
					{
						Name:        "restore-rule",
						PodSelector: "app=restore-app",
					},
				},
			}),
			expectErrContains: []string{
				"failed to process restore rule restore-rule: failed to get the absolute path of pod restore-pod: path failed",
			},
			mockPathErr: fmt.Errorf("path failed"),
		},
		{
			name: "override rule",
			metaServer: generateTestMetaServer([]*v1.Pod{
				generateTestPod("override-pod", "override-pod", map[string]string{"app": "override-app"}, []v1.Container{
					{Name: "c1"},
				}),
			}),
			config: generateTestDynamicConfig(&finegrainedresource.CPUWeightConfiguration{
				OverrideRules: []finegrainedresource.CPUWeightOverride{
					{
						Name:         "override-rule",
						PodSelector:  "app=override-app",
						PodCPUDemand: 4,
					},
				},
			}),
			expectShares: []uint64{4096},
			expectMetrics: []metricRecord{
				{
					name: util.MetricNameCPUWeightSucceedShares,
					val:  4096,
					tags: map[string]string{"rule_type": "override", "rule_name": "override-rule", "rule_pod": "override-pod"},
				},
			},
		},
		{
			name: "override apply error",
			metaServer: generateTestMetaServer([]*v1.Pod{
				generateTestPod("override-pod", "override-pod", map[string]string{"app": "override-app"}, []v1.Container{
					{Name: "c1"},
				}),
			}),
			config: generateTestDynamicConfig(&finegrainedresource.CPUWeightConfiguration{
				OverrideRules: []finegrainedresource.CPUWeightOverride{
					{
						Name:         "override-rule",
						PodSelector:  "app=override-app",
						PodCPUDemand: 4,
					},
				},
			}),
			expectErrContains: []string{
				"failed to process override rule override-rule: failed to apply cpu weight for pod override-pod: apply failed",
			},
			mockApplyErr: fmt.Errorf("apply failed"),
			expectNoMetrics: []metricRecord{
				{
					name: util.MetricNameCPUWeightSucceedShares,
					tags: map[string]string{"rule_type": "override", "rule_name": "override-rule", "rule_pod": "override-pod"},
				},
			},
		},
		{
			name: "override partial failure emits metric only for successful pod",
			metaServer: generateTestMetaServer([]*v1.Pod{
				generateTestPod("override-pod-1", "override-pod-1", map[string]string{"app": "override-app"}, []v1.Container{
					{Name: "c1"},
				}),
				generateTestPod("override-pod-2", "override-pod-2", map[string]string{"app": "override-app"}, []v1.Container{
					{Name: "c1"},
				}),
			}),
			config: generateTestDynamicConfig(&finegrainedresource.CPUWeightConfiguration{
				OverrideRules: []finegrainedresource.CPUWeightOverride{
					{
						Name:         "override-rule",
						PodSelector:  "app=override-app",
						PodCPUDemand: 4,
					},
				},
			}),
			expectShares:      []uint64{4096, 4096},
			expectErrContains: []string{"failed to process override rule override-rule: failed to apply cpu weight for pod override-pod-2: apply failed"},
			mockApplyErrs:     []error{nil, fmt.Errorf("apply failed")},
			expectMetrics: []metricRecord{
				{
					name: util.MetricNameCPUWeightSucceedShares,
					val:  4096,
					tags: map[string]string{"rule_type": "override", "rule_name": "override-rule", "rule_pod": "override-pod-1"},
				},
			},
			expectNoMetrics: []metricRecord{
				{
					name: util.MetricNameCPUWeightSucceedShares,
					tags: map[string]string{"rule_type": "override", "rule_name": "override-rule", "rule_pod": "override-pod-2"},
				},
			},
		},
	}

	for _, tt := range tests {
		mockey.PatchConvey(tt.name, t, func() {
			emitter := &recordingEmitter{}
			manager := &managerImpl{metaServer: tt.metaServer, emitter: emitter}
			if tt.expectShares == nil && tt.expectErrContains == nil && tt.mockPathErr == nil && tt.mockApplyErr == nil {
				assert.NoError(t, manager.UpdateCPUWeight(tt.config))
				return
			}

			runAndAssert := func() {
				err := manager.UpdateCPUWeight(tt.config)
				if len(tt.expectErrContains) == 0 {
					assert.NoError(t, err)
				} else {
					assert.Error(t, err)
					for _, expected := range tt.expectErrContains {
						assert.ErrorContains(t, err, expected)
					}
				}
			}

			if tt.expectShares == nil && tt.mockPathErr == nil && tt.mockApplyErr == nil {
				runAndAssert()
				return
			}

			podCgroupPath := t.TempDir()

			var gotShares []uint64
			pathMock := mockey.Mock(common.GetPodAbsCgroupPath).IncludeCurrentGoRoutine()
			if tt.mockPathErr != nil {
				pathMock.Return("", tt.mockPathErr).Build()
			} else {
				pathMock.Return(podCgroupPath, nil).Build()
			}

			applyCallCount := 0
			mockey.Mock(cgroupmgr.ApplyCPUWithAbsolutePath).IncludeCurrentGoRoutine().To(func(_ string, cpuData *common.CPUData) error {
				gotShares = append(gotShares, cpuData.Shares)
				if applyCallCount < len(tt.mockApplyErrs) {
					err := tt.mockApplyErrs[applyCallCount]
					applyCallCount++
					return err
				}
				applyCallCount++
				return tt.mockApplyErr
			}).Build()

			runAndAssert()
			if len(tt.expectShares) > 0 {
				assert.Equal(t, tt.expectShares, gotShares)
			}
			for _, expectedMetric := range tt.expectMetrics {
				assertMetricRecord(t, emitter.records, expectedMetric.name, expectedMetric.val, expectedMetric.tags)
			}
			for _, expectedMetric := range tt.expectNoMetrics {
				assertNoMetricRecord(t, emitter.records, expectedMetric.name, expectedMetric.tags)
			}
		})
	}
}

func TestManager_getPodOriginalCPURequests(t *testing.T) {
	t.Parallel()

	manager := &managerImpl{}

	tests := []struct {
		name     string
		pod      *v1.Pod
		expected int64
	}{
		{
			name: "from annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						DemandCoresAnnotationKey: "8",
					},
				},
			},
			expected: 8 * milliCPUPerCPU,
		},
		{
			name: "from pod request sum",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU: resource.MustParse("1"),
								},
							},
						},
						{
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU: resource.MustParse("3"),
								},
							},
						},
					},
				},
			},
			expected: 4 * milliCPUPerCPU,
		},
		{
			name: "annotation invalid falls back to spec",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						DemandCoresAnnotationKey: "invalid",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU: resource.MustParse("2"),
								},
							},
						},
					},
				},
			},
			expected: 2 * milliCPUPerCPU,
		},
		{
			name: "no annotation and no request",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: 0,
		},
		{
			name: "annotation with milli value",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						DemandCoresAnnotationKey: "500m",
					},
				},
			},
			expected: 500,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := manager.getPodOriginalCPURequests(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestManager_getMilliCPUFromAnnotation(t *testing.T) {
	t.Parallel()

	manager := &managerImpl{}

	tests := []struct {
		name        string
		annotations map[string]string
		expected    int64
	}{
		{
			name:        "nil annotations",
			annotations: nil,
			expected:    0,
		},
		{
			name:        "annotation not present",
			annotations: map[string]string{"other": "value"},
			expected:    0,
		},
		{
			name: "empty annotation value",
			annotations: map[string]string{
				DemandCoresAnnotationKey: "",
			},
			expected: 0,
		},
		{
			name: "valid integer value",
			annotations: map[string]string{
				DemandCoresAnnotationKey: "8",
			},
			expected: 8 * milliCPUPerCPU,
		},
		{
			name: "valid milli value",
			annotations: map[string]string{
				DemandCoresAnnotationKey: "4000m",
			},
			expected: 4 * milliCPUPerCPU,
		},
		{
			name: "valid milli value with burst ratio",
			annotations: map[string]string{
				DemandCoresAnnotationKey: "4000m",
				BurstRatioAnnotationKey:  "2",
			},
			expected: 8 * milliCPUPerCPU,
		},
		{
			name: "invalid value",
			annotations: map[string]string{
				DemandCoresAnnotationKey: "abc",
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testPod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tt.annotations,
				},
			}
			result := manager.getMilliCPUFromAnnotation(testPod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCPUWeight_milliCPUToShares(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		milliCPU int64
		expected uint64
	}{
		{
			name:     "1 core",
			milliCPU: 1 * milliCPUPerCPU,
			expected: 1024,
		},
		{
			name:     "2 cores",
			milliCPU: 2 * milliCPUPerCPU,
			expected: 2048,
		},
		{
			name:     "64 cores",
			milliCPU: 64 * milliCPUPerCPU,
			expected: 65536,
		},
		{
			name:     "128 cores",
			milliCPU: 128 * milliCPUPerCPU,
			expected: 131072,
		},
		{
			name:     "256 cores",
			milliCPU: 256 * milliCPUPerCPU,
			expected: 262144,
		},
		{
			name:     "512 cores (exceeds max)",
			milliCPU: 512 * milliCPUPerCPU,
			expected: 262144,
		},
		{
			name:     "0 cores (minimum)",
			milliCPU: 0,
			expected: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := milliCPUToShares(tt.milliCPU)
			assert.Equalf(t, tt.expected, result, "expected %v, got %v", tt.expected, result)
		})
	}
}
