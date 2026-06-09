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
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	irqutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	podagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/resourcepackage"
	cgroupcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type dummyIRQTuner struct{}

func (t *dummyIRQTuner) Run(_ <-chan struct{}) {}
func (t *dummyIRQTuner) Stop()                 {}

func newTestDynamicPolicy(t *testing.T, name string) *DynamicPolicy {
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-"+name)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	policyImpl, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	return policyImpl
}

var registerRelativeCgroupPathHandlerOnce sync.Once

func registerRelativeCgroupPathHandler(podUID string) {
	registerRelativeCgroupPathHandlerOnce.Do(func() {
		cgroupcommon.RegisterRelativeCgroupPathHandler(cgroupcommon.RelativeCgroupPathHandler{
			Name: "unit_test",
			Handler: func(pUID, containerID string) (string, error) {
				if pUID != podUID {
					return "", fmt.Errorf("pod uid mismatch")
				}
				if containerID != "cid0" && containerID != "cid1" {
					return "", fmt.Errorf("container id mismatch")
				}
				return fmt.Sprintf("/unit-test/%s/%s", pUID, containerID), nil
			},
		})
	})
}

func TestDynamicPolicy_SetIRQTuner(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	policyImpl := newTestDynamicPolicy(t, "set-irq-tuner")
	tuner := &dummyIRQTuner{}

	policyImpl.SetIRQTuner(tuner)
	as.Same(tuner, policyImpl.irqTuner)
}

func TestDynamicPolicy_getPodContainerInfos(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	policyImpl := newTestDynamicPolicy(t, "get-pod-container-infos")

	podUID := "test-pod-uid"
	registerRelativeCgroupPathHandler(podUID)

	runtimeClassName := "kata"
	startedAt := metav1.NewTime(time.Now())

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:         types.UID(podUID),
			Name:        "test-pod",
			Namespace:   "test-ns",
			Annotations: map[string]string{"pod-anno": "pod"},
		},
		Spec: v1.PodSpec{RuntimeClassName: &runtimeClassName},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:        "c0",
					ContainerID: "containerd://cid0",
					State: v1.ContainerState{
						Running: &v1.ContainerStateRunning{StartedAt: startedAt},
					},
				},
				{
					Name:        "c1",
					ContainerID: "containerd://cid1",
					State: v1.ContainerState{
						Waiting: &v1.ContainerStateWaiting{},
					},
				},
				{
					Name:        "c2",
					ContainerID: "",
					State: v1.ContainerState{
						Running: &v1.ContainerStateRunning{StartedAt: startedAt},
					},
				},
			},
		},
	}

	policyImpl.metaServer.MetaAgent.PodFetcher = &podagent.PodFetcherStub{PodList: []*v1.Pod{pod}}

	allocationInfo0 := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "test-ns",
			PodName:       "test-pod",
			ContainerName: "c0",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			Annotations:   map[string]string{"alloc-anno": "alloc"},
		},
		TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(1)},
	}

	allocationInfo1 := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "test-ns",
			PodName:       "test-pod",
			ContainerName: "c1",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
		},
		TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(2)},
	}

	allocationInfo2 := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "test-ns",
			PodName:       "test-pod",
			ContainerName: "c2",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
		},
	}

	entry := state.ContainerEntries{
		"c0": allocationInfo0,
		"c1": allocationInfo1,
		"c2": allocationInfo2,
		"c3": nil,
	}

	advisorTestMutex.Lock()
	defer advisorTestMutex.Unlock()

	cis, err := policyImpl.getPodContainerInfos(podUID, entry)
	as.NoError(err)
	as.Len(cis, 1)

	ci := cis[0]
	as.Equal("cid0", ci.ContainerID)
	as.Equal(fmt.Sprintf("/unit-test/%s/%s", podUID, "cid0"), ci.CgroupPath)
	as.Equal(runtimeClassName, ci.RuntimeClassName)
	as.Equal(startedAt, ci.StartedAt)
	as.Equal(podUID, ci.PodUid)
	as.Equal("test-ns", ci.PodNamespace)
	as.Equal("test-pod", ci.PodName)
	as.Equal("c0", ci.ContainerName)
	as.Contains(ci.Annotations, "pod-anno")
	as.Contains(ci.Annotations, "alloc-anno")
}

func TestDynamicPolicy_ListContainers(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	policyImpl := newTestDynamicPolicy(t, "list-containers")

	podUID := "test-pod-uid"
	registerRelativeCgroupPathHandler(podUID)

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:         types.UID(podUID),
			Name:        "test-pod",
			Namespace:   "test-ns",
			Annotations: map[string]string{"pod-anno": "pod"},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:        "c0",
					ContainerID: "containerd://cid0",
					State: v1.ContainerState{
						Running: &v1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now())},
					},
				},
			},
		},
	}

	policyImpl.metaServer.MetaAgent.PodFetcher = &podagent.PodFetcherStub{PodList: []*v1.Pod{pod}}

	podEntries := state.PodEntries{
		podUID: {
			"c0": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        podUID,
					PodNamespace:  "test-ns",
					PodName:       "test-pod",
					ContainerName: "c0",
					ContainerType: pluginapi.ContainerType_MAIN.String(),
				},
				TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(1)},
			},
		},
		"non-exist-pod": {
			"c0": &state.AllocationInfo{},
		},
		"pool-entry": {
			commonstate.FakedContainerName: &state.AllocationInfo{},
		},
	}
	policyImpl.state.SetPodEntries(podEntries, true)

	cis, err := policyImpl.ListContainers()
	as.NoError(err)
	as.Len(cis, 1)
	as.Equal("cid0", cis[0].ContainerID)
}

func TestDynamicPolicy_GetIRQForbiddenCores(t *testing.T) {
	t.Parallel()

	// setupPolicy builds a fresh DynamicPolicy with reserved CPUs (0,1) and a
	// pinned-forbidden resource package on NUMA 0 (CPUs 2,3). pkg2 (allowed) on
	// NUMA 1 (CPUs 4,5) is filtered out by the selector.
	setupPolicy := func(t *testing.T, name string) *DynamicPolicy {
		t.Helper()
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetIRQForbiddenCores-"+name)
		require.NoError(t, err)
		t.Cleanup(func() { os.RemoveAll(tmpDir) })

		cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
		require.NoError(t, err)

		policy, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
		require.NoError(t, err)

		policy.reservedCPUs = machine.NewCPUSet(0, 1)

		npdFetcher := &npd.DummyNPDFetcher{
			NPD: &nodev1alpha1.NodeProfileDescriptor{
				Status: nodev1alpha1.NodeProfileDescriptorStatus{
					NodeMetrics: []nodev1alpha1.ScopedNodeMetrics{
						{
							Scope: "resource-package",
							Metrics: []nodev1alpha1.MetricValue{
								{
									MetricName: string(v1.ResourceCPU),
									MetricLabels: map[string]string{
										"package-name":  "pkg1",
										"numa-id":       "0",
										"pinned-cpuset": "true",
										"type":          "forbidden",
									},
									Value:      *resource.NewQuantity(2, resource.DecimalSI),
									Aggregator: func() *nodev1alpha1.Aggregator { a := nodev1alpha1.AggregatorMin; return &a }(),
								},
								{
									MetricName: string(v1.ResourceCPU),
									MetricLabels: map[string]string{
										"package-name":  "pkg2",
										"numa-id":       "1",
										"pinned-cpuset": "true",
										"type":          "allowed",
									},
									Value:      *resource.NewQuantity(2, resource.DecimalSI),
									Aggregator: func() *nodev1alpha1.Aggregator { a := nodev1alpha1.AggregatorMin; return &a }(),
								},
							},
						},
					},
				},
			},
		}
		policy.resourcePackageManager = resourcepackage.NewCachedResourcePackageManager(resourcepackage.NewResourcePackageManager(npdFetcher))
		stopCh := make(chan struct{})
		t.Cleanup(func() { close(stopCh) })
		go policy.resourcePackageManager.Run(stopCh)
		time.Sleep(100 * time.Millisecond)

		machineState := policy.state.GetMachineState()
		machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
			"pkg1": {
				PinnedCPUSet: machine.NewCPUSet(2, 3),
				Attributes:   map[string]string{"type": "forbidden"},
			},
		}
		machineState[1].ResourcePackageStates = map[string]*state.ResourcePackageState{
			"pkg2": {
				PinnedCPUSet: machine.NewCPUSet(4, 5),
				Attributes:   map[string]string{"type": "other"},
			},
		}
		policy.state.SetMachineState(machineState, false)

		selector, err := labels.Parse("type=forbidden")
		require.NoError(t, err)
		policy.conf.IRQForbiddenPinnedResourcePackageAttributeSelector = selector

		return policy
	}

	// All non-reserved CPUs in the dummy 16-CPU topology are 2..15 (reserved is 0,1).
	allNonReserved := machine.NewCPUSet(2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)

	tcs := []struct {
		name              string
		mode              string
		excludeIsolCPUs   bool
		isolatedCPUs      machine.CPUSet
		expectedForbidden machine.CPUSet
	}{
		{
			name:              "non-reserved mode, isolcpus disabled",
			mode:              irqutil.IRQAffinityModeNonReserved,
			excludeIsolCPUs:   false,
			isolatedCPUs:      machine.NewCPUSet(),
			expectedForbidden: machine.NewCPUSet(0, 1, 2, 3),
		},
		{
			name:              "reserved-only mode, isolcpus disabled",
			mode:              irqutil.IRQAffinityModeReservedOnly,
			excludeIsolCPUs:   false,
			isolatedCPUs:      machine.NewCPUSet(),
			expectedForbidden: allNonReserved.Union(machine.NewCPUSet(2, 3)),
		},
		{
			name:              "exclude isolcpus enabled but IsolatedCPUs empty",
			mode:              irqutil.IRQAffinityModeNonReserved,
			excludeIsolCPUs:   true,
			isolatedCPUs:      machine.NewCPUSet(),
			expectedForbidden: machine.NewCPUSet(0, 1, 2, 3),
		},
		{
			name:              "non-reserved mode + isolcpus 6,7",
			mode:              irqutil.IRQAffinityModeNonReserved,
			excludeIsolCPUs:   true,
			isolatedCPUs:      machine.NewCPUSet(6, 7),
			expectedForbidden: machine.NewCPUSet(0, 1, 2, 3, 6, 7),
		},
		{
			name:              "reserved-only mode + isolcpus 6,7",
			mode:              irqutil.IRQAffinityModeReservedOnly,
			excludeIsolCPUs:   true,
			isolatedCPUs:      machine.NewCPUSet(6, 7),
			expectedForbidden: allNonReserved.Union(machine.NewCPUSet(2, 3)),
		},
	}

	for _, tc := range tcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy := setupPolicy(t, tc.name)
			policy.conf.IRQAffinityMode = tc.mode
			policy.conf.ExcludeIsolCPUsFromIRQ = tc.excludeIsolCPUs
			policy.machineInfo.IsolatedCPUs = tc.isolatedCPUs

			forbiddenCores, err := policy.GetIRQForbiddenCores()
			require.NoError(t, err)
			assert.True(t, tc.expectedForbidden.Equals(forbiddenCores),
				"case %q: expected %v, got %v", tc.name, tc.expectedForbidden, forbiddenCores)
		})
	}
}

func TestDynamicPolicy_GetExclusiveIRQCPUSet(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	policyImpl := newTestDynamicPolicy(t, "get-exclusive-irq-cpuset")

	irqCPUSet, err := policyImpl.GetExclusiveIRQCPUSet()
	as.NoError(err)
	as.True(irqCPUSet.IsEmpty())

	expectedCPUSet := machine.NewCPUSet(1, 2, 3, 4)
	podEntries := policyImpl.state.GetPodEntries()
	podEntries[commonstate.PoolNameInterrupt] = state.ContainerEntries{commonstate.FakedContainerName: &state.AllocationInfo{
		AllocationResult: expectedCPUSet,
	}}
	policyImpl.state.SetPodEntries(podEntries, true)

	irqCPUSet, err = policyImpl.GetExclusiveIRQCPUSet()
	as.NoError(err)
	as.Equal(irqCPUSet, expectedCPUSet)
}

func TestDynamicPolicy_SetExclusiveIRQCPUSet(t *testing.T) {
	t.Parallel()

	t.Run("exceed max expandable capacity", func(t *testing.T) {
		t.Parallel()

		as := require.New(t)
		policyImpl := newTestDynamicPolicy(t, "set-exclusive-irq-cpuset-1")

		available := policyImpl.state.GetMachineState().GetAvailableCPUSet(policyImpl.reservedCPUs)
		maxExpandableSize := int(math.Ceil(float64(available.Size()) * irqutil.DefaultIRQExclusiveMaxExpansionRate))
		as.Greater(maxExpandableSize, 0)

		irqCPUSet := machine.NewCPUSet(available.ToSliceInt()[:maxExpandableSize]...)
		err := policyImpl.SetExclusiveIRQCPUSet(irqCPUSet)
		as.ErrorIs(err, irqutil.ExceededMaxExpandableCapacityErr)
	})

	t.Run("exceed max step expandable capacity", func(t *testing.T) {
		t.Parallel()

		as := require.New(t)
		policyImpl := newTestDynamicPolicy(t, "set-exclusive-irq-cpuset-2")

		available := policyImpl.state.GetMachineState().GetAvailableCPUSet(policyImpl.reservedCPUs)
		maxExpandableSize := int(math.Ceil(float64(available.Size()) * irqutil.DefaultIRQExclusiveMaxExpansionRate))
		maxStepExpandableSize := policyImpl.GetStepExpandableCPUsMax()
		as.Greater(maxExpandableSize, maxStepExpandableSize+1)

		irqCPUSet := machine.NewCPUSet(available.ToSliceInt()[:maxStepExpandableSize+1]...)
		err := policyImpl.SetExclusiveIRQCPUSet(irqCPUSet)
		as.ErrorIs(err, irqutil.ExceededMaxStepExpandableCapacityErr)
	})

	t.Run("contain forbidden cpu", func(t *testing.T) {
		t.Parallel()

		as := require.New(t)
		policyImpl := newTestDynamicPolicy(t, "set-exclusive-irq-cpuset-3")

		reservedCPU := []int{2, 4}
		policyImpl.reservedCPUs = machine.NewCPUSet(reservedCPU...)
		forbidden, err := policyImpl.GetIRQForbiddenCores()
		as.NoError(err)
		as.True(forbidden.Equals(policyImpl.reservedCPUs))

		irqCPUSet := machine.NewCPUSet(forbidden.ToSliceInt()[0])
		err = policyImpl.SetExclusiveIRQCPUSet(irqCPUSet)
		as.ErrorIs(err, irqutil.ContainForbiddenCPUErr)
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		as := require.New(t)
		policyImpl := newTestDynamicPolicy(t, "set-exclusive-irq-cpuset-4")

		available := policyImpl.state.GetMachineState().GetAvailableCPUSet(policyImpl.reservedCPUs)
		as.Greater(available.Size(), 0)

		irqCPUSet := machine.NewCPUSet(available.ToSliceInt()[0])
		err := policyImpl.SetExclusiveIRQCPUSet(irqCPUSet)
		as.NoError(err)

		got, err := policyImpl.GetExclusiveIRQCPUSet()
		as.NoError(err)
		as.True(got.Equals(irqCPUSet))

		podEntries := policyImpl.state.GetPodEntries()
		as.NotNil(podEntries[commonstate.PoolNameInterrupt][commonstate.FakedContainerName])
		as.True(podEntries[commonstate.PoolNameInterrupt][commonstate.FakedContainerName].AllocationResult.Equals(irqCPUSet))
	})
}
