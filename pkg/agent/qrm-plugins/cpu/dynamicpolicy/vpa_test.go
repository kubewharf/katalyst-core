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
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSNBVPA(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSNBVPA")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.podAnnotationKeptKeys = []string{consts.PodAnnotationInplaceUpdateResizingKey}

	testName := "test"

	// allocate container
	req := &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
		},
	}

	res, err := dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	hints := res.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(hints))

	req = &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
		},
		Hint: hints[0],
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	resp1, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resp1.PodResources[req.PodUid])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 3, // 分配到numa0    (cpu0 -> reserved, cpu1,cpu8,cpu9 for snb)
		AllocationResult:  "1,8-9",
	}, resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	// resize exceed
	resizeReq := &pluginapi.ResourceRequest{
		PodUid:         req.PodUid,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 4, // greater than pool size
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
		},
	}

	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.NotNil(err)

	// resize successfully
	resizeReq1 := &pluginapi.ResourceRequest{
		PodUid:         req.PodUid,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 3, // greater than pool size
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
		},
	}

	resizeResp1, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeReq1)
	as.Nil(err)
	resizeHints1 := resizeResp1.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(resizeHints1))

	resizeReq1 = &pluginapi.ResourceRequest{
		PodUid:         req.PodUid,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 3, // greater than pool size
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
		},
		Hint: resizeHints1[0],
	}

	_, err = dynamicPolicy.Allocate(context.Background(), resizeReq1)
	as.Nil(err)

	resp1, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resp1.PodResources[req.PodUid])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 3, // 分配到numa0    (cpu0 -> reserved, cpu1,cpu8,cpu9 for snb)
		AllocationResult:  "1,8-9",
	}, resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
}

func TestSNBVPAWithSidecar(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSNBVPAWithSidecar")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(48, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.podAnnotationKeptKeys = []string{
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	podNamespace := "test"
	podName := "test"
	mainContainerName := "main"
	sidecarContainerName := "sidecar"

	podUID := string(uuid.NewUUID())
	// admit sidecar firstly
	sidecarReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  sidecarContainerName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 1,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: "{\"cpu\":\"3\"}",
		},
	}

	// no hints for sidecar
	sidecarRes, err := dynamicPolicy.GetTopologyHints(context.Background(), sidecarReq)
	as.Nil(err)
	as.Nil(sidecarRes.ResourceHints[string(v1.ResourceCPU)])

	// main container doesn't allocate, return nil
	sidecarReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  sidecarContainerName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 1,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: "{\"cpu\":\"3\"}",
		},
	}
	sidecarAllocateRes, err := dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)
	as.Nil(sidecarAllocateRes.AllocationResult)

	// no sidecar container record here, because main container doesn't allocate now
	allocationRes, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	_, exist := allocationRes.PodResources[podUID]
	as.Equal(false, exist)

	mainReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  mainContainerName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: "{\"cpu\":\"3\"}",
		},
	}

	mainRes, err := dynamicPolicy.GetTopologyHints(context.Background(), mainReq)
	as.Nil(err)
	hints := mainRes.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(hints))

	mainReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  mainContainerName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: "{\"cpu\":\"3\"}",
		},
		Hint: hints[0],
	}

	_, err = dynamicPolicy.Allocate(context.Background(), mainReq)
	as.Nil(err)

	allocationRes, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(allocationRes.PodResources[mainReq.PodUid])
	// check main container
	as.NotNil(allocationRes.PodResources[mainReq.PodUid].ContainerResources[mainContainerName])
	as.NotNil(allocationRes.PodResources[mainReq.PodUid].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 11, // 分配到numa0    (cpu0 -> reserved, cpu1~cpu5,cpu24~cpu29 for snb)
		AllocationResult:  "1-5,24-29",
	}, allocationRes.PodResources[mainReq.PodUid].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	// reallocate for sidecar
	// no hints for sidecar
	sidecarRes, err = dynamicPolicy.GetTopologyHints(context.Background(), sidecarReq)
	as.Nil(err)
	as.Nil(sidecarRes.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)

	allocationRes, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	// check sidecar container (follow main container)
	as.NotNil(allocationRes.PodResources[mainReq.PodUid].ContainerResources[sidecarContainerName])
	as.NotNil(allocationRes.PodResources[mainReq.PodUid].ContainerResources[sidecarContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 11, // 分配到numa0    (cpu0 -> reserved, cpu1~cpu5,cpu24~cpu29 for snb)
		AllocationResult:  "1-5,24-29",
	}, allocationRes.PodResources[mainReq.PodUid].ContainerResources[sidecarContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	// check container allocation request
	mainContainerAllocation := dynamicPolicy.state.GetAllocationInfo(podUID, mainContainerName)
	as.Equal(float64(2), mainContainerAllocation.RequestQuantity)
	sidecarContainerAllocation := dynamicPolicy.state.GetAllocationInfo(podUID, sidecarContainerName)
	as.Equal(float64(1), sidecarContainerAllocation.RequestQuantity)
	// check pod aggregated request
	aggregatedRequest, ok := mainContainerAllocation.GetPodAggregatedRequest()
	as.Equal(true, ok)
	as.Equal(float64(3), aggregatedRequest)

	resizeMainContainerReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  mainContainerName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 3,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationAggregatedRequestsKey:    "{\"cpu\":\"4\"}",
		},
	}

	resizeMainContainerResp, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeMainContainerReq)
	as.Nil(err)
	resizeMainContainerHints := resizeMainContainerResp.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(resizeMainContainerHints))
	resizeMainContainerReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  mainContainerName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 3,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationAggregatedRequestsKey:    "{\"cpu\":\"4\"}",
		},
		Hint: resizeMainContainerHints[0],
	}
	_, err = dynamicPolicy.Allocate(context.Background(), resizeMainContainerReq)
	as.Nil(err)

	resizeMainContainerAllocations, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resizeMainContainerAllocations.PodResources[podUID])
	as.NotNil(resizeMainContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName])
	as.NotNil(resizeMainContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 11, // 分配到numa0    (cpu0 -> reserved, cpu1~cpu5,cpu24~cpu29 for snb)
		AllocationResult:  "1-5,24-29",
	}, resizeMainContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	as.NotNil(resizeMainContainerAllocations.PodResources[podUID].ContainerResources[sidecarContainerName])
	as.NotNil(resizeMainContainerAllocations.PodResources[podUID].ContainerResources[sidecarContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 11, // 分配到numa0    (cpu0 -> reserved, cpu1~cpu5,cpu24~cpu29 for snb)
		AllocationResult:  "1-5,24-29",
	}, resizeMainContainerAllocations.PodResources[podUID].ContainerResources[sidecarContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	mainContainerAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, mainContainerName)
	as.Equal(float64(3), mainContainerAllocation.RequestQuantity)
	sidecarContainerAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, sidecarContainerName)
	as.Equal(float64(1), sidecarContainerAllocation.RequestQuantity)

	// check pod aggregated request
	aggregatedRequest, ok = mainContainerAllocation.GetPodAggregatedRequest()
	as.Equal(true, ok)
	as.Equal(float64(4), aggregatedRequest)

	// resize sidecar
	resizeSidecarContainerReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  sidecarContainerName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationAggregatedRequestsKey:    "{\"cpu\":\"5\"}",
		},
	}

	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeSidecarContainerReq)
	as.Nil(err)

	resizeSidecarContainerReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  sidecarContainerName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationAggregatedRequestsKey:    "{\"cpu\":\"5\"}",
		},
	}
	_, err = dynamicPolicy.Allocate(context.Background(), resizeSidecarContainerReq)
	as.Nil(err)

	resizeMainContainerReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  mainContainerName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 3,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationAggregatedRequestsKey:    "{\"cpu\":\"5\"}",
		},
	}
	resizeMainContainerResp, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeMainContainerReq)
	as.Nil(err)

	resizeMainContainerReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  mainContainerName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 3,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationAggregatedRequestsKey:    "{\"cpu\":\"5\"}",
		},
		Hint: resizeMainContainerResp.ResourceHints[string(v1.ResourceCPU)].Hints[0],
	}
	_, err = dynamicPolicy.Allocate(context.Background(), resizeMainContainerReq)
	as.Nil(err)

	resizeSidecarContainerAllocations, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID])
	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName])
	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 11, // 分配到numa0    (cpu0 -> reserved, cpu1~cpu5,cpu24~cpu29 for snb)
		AllocationResult:  "1-5,24-29",
	}, resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName])
	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 11, // 分配到numa0    (cpu0 -> reserved, cpu1~cpu5,cpu24~cpu29 for snb)
		AllocationResult:  "1-5,24-29",
	}, resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	mainContainerAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, mainContainerName)
	as.Equal(float64(3), mainContainerAllocation.RequestQuantity)
	sidecarContainerAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, sidecarContainerName)
	as.Equal(float64(2), sidecarContainerAllocation.RequestQuantity)

	// check pod aggregated request
	aggregatedRequest, ok = mainContainerAllocation.GetPodAggregatedRequest()
	as.Equal(true, ok)
	as.Equal(float64(5), aggregatedRequest)

	// resize main exceed
	resizeMainContainerReq.ResourceRequests[string(v1.ResourceCPU)] = 10
	resizeMainContainerReq.Annotations[consts.PodAnnotationAggregatedRequestsKey] = "{\"cpu\":\"12\"}"
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeMainContainerReq)
	as.Nil(err)

	// resize sidecar exceed
	resizeSidecarContainerReq.ResourceRequests[string(v1.ResourceCPU)] = 10
	resizeSidecarContainerReq.Annotations[consts.PodAnnotationAggregatedRequestsKey] = "{\"cpu\":\"13\"}"
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeSidecarContainerReq)
	as.Nil(err)
}

func TestNormalShareVPA(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestNormalShareVPA")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.podAnnotationKeptKeys = []string{consts.PodAnnotationMemoryEnhancementNumaBinding, consts.PodAnnotationInplaceUpdateResizingKey}
	dynamicPolicy.transitionPeriod = 10 * time.Millisecond

	testName := "test"

	// test for normal share
	req := &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	// no topology hints for normal share
	res, err := dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	as.Nil(res.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	time.Sleep(20 * time.Millisecond)

	resp1, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	reclaim := dynamicPolicy.state.GetAllocationInfo(state.PoolNameReclaim, state.FakedContainerName)
	as.NotNil(reclaim)

	as.NotNil(resp1.PodResources[req.PodUid])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	// share pool size: 10, reclaimed pool size: 4, reserved pool size: 2
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 10,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).Difference(reclaim.AllocationResult).String(),
	}, resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	resizeReq := &pluginapi.ResourceRequest{
		PodUid:         req.PodUid,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 16, // greater than pool size
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
		},
	}

	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.ErrorContains(err, "no enough")

	resizeReq1 := &pluginapi.ResourceRequest{
		PodUid:         req.PodUid,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 3, // greater than pool size
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
		},
	}

	resizeResp1, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeReq1)
	as.Nil(err)
	// no hints for normal share
	as.Nil(resizeResp1.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), resizeReq1)
	as.Nil(err)

	resp1, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	reclaim = dynamicPolicy.state.GetAllocationInfo(state.PoolNameReclaim, state.FakedContainerName)
	as.NotNil(reclaim)

	as.NotNil(resp1.PodResources[req.PodUid])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	// 10 = 16 - 2(reserved) - 4(reclaimed)
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 10,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).Difference(reclaim.AllocationResult).String(),
	}, resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	allocation := dynamicPolicy.state.GetAllocationInfo(req.PodUid, testName)
	as.NotNil(allocation)
	as.Equal(float64(3), allocation.RequestQuantity)
}

func TestNormalShareVPAWithSidecar(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestNormalShareVPAWithSidecar")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(48, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)
	dynamicPolicy.state.SetAllowSharedCoresOverlapReclaimedCores(false)
	dynamicPolicy.transitionPeriod = 10 * time.Millisecond

	dynamicPolicy.podAnnotationKeptKeys = []string{
		consts.PodAnnotationMemoryEnhancementNumaBinding,
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	podNamespace := "test"
	podName := "test"
	mainContainerName := "main"
	sidecarContainerName := "sidecar"

	podUID := string(uuid.NewUUID())
	// admit sidecar firstly
	sidecarReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  sidecarContainerName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 1,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationAggregatedRequestsKey: "{\"cpu\":\"3\"}",
		},
	}

	// no hints for share cores container (sidecar and main container)
	sidecarRes, err := dynamicPolicy.GetTopologyHints(context.Background(), sidecarReq)
	as.Nil(err)
	as.Nil(sidecarRes.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)

	time.Sleep(20 * time.Millisecond)

	// there is sidecar hints for sidecar container here, and it was bound to the whole share pool
	allocationRes, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	_, exist := allocationRes.PodResources[podUID]
	as.Equal(true, exist)
	as.NotNil(allocationRes.PodResources[podUID].ContainerResources[sidecarContainerName])
	as.NotNil(allocationRes.PodResources[podUID].ContainerResources[sidecarContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	// reserve pool size: 2, reclaimed pool size: 4, share pool size: 42
	reclaim := dynamicPolicy.state.GetAllocationInfo(state.PoolNameReclaim, state.FakedContainerName)
	as.NotNil(reclaim)
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 42,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).Difference(reclaim.AllocationResult).String(),
	}, allocationRes.PodResources[podUID].ContainerResources[sidecarContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	mainReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  mainContainerName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationAggregatedRequestsKey: "{\"cpu\":\"3\"}",
		},
	}

	// there is no hints for share cores container (sidecar and main container)
	mainRes, err := dynamicPolicy.GetTopologyHints(context.Background(), mainReq)
	as.Nil(err)
	as.Nil(mainRes.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), mainReq)
	as.Nil(err)

	// sleep to finish main container ramp up
	time.Sleep(20 * time.Millisecond)
	allocationRes, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	// main container was bound to whole share pool now
	as.NotNil(allocationRes.PodResources[mainReq.PodUid])
	// check main container
	as.NotNil(allocationRes.PodResources[mainReq.PodUid].ContainerResources[mainContainerName])
	as.NotNil(allocationRes.PodResources[mainReq.PodUid].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	// reserve pool size: 2, reclaimed pool size: 4, share pool size: 42
	reclaim = dynamicPolicy.state.GetAllocationInfo(state.PoolNameReclaim, state.FakedContainerName)
	as.NotNil(reclaim)
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 42,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).Difference(reclaim.AllocationResult).String(),
	}, allocationRes.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	// no reallocate for share cores sidecar

	// check container allocation request
	mainContainerAllocation := dynamicPolicy.state.GetAllocationInfo(podUID, mainContainerName)
	as.Equal(float64(2), mainContainerAllocation.RequestQuantity)
	sidecarContainerAllocation := dynamicPolicy.state.GetAllocationInfo(podUID, sidecarContainerName)
	as.Equal(float64(1), sidecarContainerAllocation.RequestQuantity)
	// check pod aggregated request
	aggregatedRequest, ok := mainContainerAllocation.GetPodAggregatedRequest()
	as.Equal(true, ok)
	as.Equal(float64(3), aggregatedRequest)

	resizeMainContainerReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  mainContainerName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 3,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationAggregatedRequestsKey:    "{\"cpu\":\"4\"}",
		},
	}

	// there is no hints for share cores container (sidecar and main container)
	resizeMainContainerResp, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeMainContainerReq)
	as.Nil(err)
	as.Nil(resizeMainContainerResp.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), resizeMainContainerReq)
	as.Nil(err)

	resizeMainContainerAllocations, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resizeMainContainerAllocations.PodResources[podUID])
	as.NotNil(resizeMainContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName])
	as.NotNil(resizeMainContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	// reserve pool size: 2, reclaimed pool size: 4, share pool size: 42
	reclaim = dynamicPolicy.state.GetAllocationInfo(state.PoolNameReclaim, state.FakedContainerName)
	as.NotNil(reclaim)
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 42,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).Difference(reclaim.AllocationResult).String(),
	}, resizeMainContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	as.NotNil(resizeMainContainerAllocations.PodResources[podUID].ContainerResources[sidecarContainerName])
	as.NotNil(resizeMainContainerAllocations.PodResources[podUID].ContainerResources[sidecarContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	// reserve pool size: 2, reclaimed pool size: 4, share pool size: 42
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 42,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).Difference(reclaim.AllocationResult).String(),
	}, resizeMainContainerAllocations.PodResources[podUID].ContainerResources[sidecarContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	mainContainerAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, mainContainerName)
	as.Equal(float64(3), mainContainerAllocation.RequestQuantity)
	sidecarContainerAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, sidecarContainerName)
	as.Equal(float64(1), sidecarContainerAllocation.RequestQuantity)

	// check pod aggregated request
	aggregatedRequest, ok = mainContainerAllocation.GetPodAggregatedRequest()
	as.Equal(true, ok)
	as.Equal(float64(4), aggregatedRequest)

	// resize sidecar
	resizeSidecarContainerReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   podNamespace,
		PodName:        podName,
		ContainerName:  sidecarContainerName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationAggregatedRequestsKey:    "{\"cpu\":\"5\"}",
		},
	}

	// resize sidecar firstly
	resizeSidecarContainerHints, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeSidecarContainerReq)
	as.Nil(err)
	as.Nil(resizeSidecarContainerHints.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), resizeSidecarContainerReq)
	as.Nil(err)

	// resize main container (only update pod aggregated request)
	resizeMainContainerReq.Annotations[consts.PodAnnotationAggregatedRequestsKey] = "{\"cpu\":\"5\"}"
	resizeMainContainerHints, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeMainContainerReq)
	as.Nil(err)
	as.Nil(resizeMainContainerHints.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), resizeMainContainerReq)
	as.Nil(err)

	resizeSidecarContainerAllocations, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID])
	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName])
	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	// reserve pool size: 2, reclaimed pool size: 4, share pool size: 42
	reclaim = dynamicPolicy.state.GetAllocationInfo(state.PoolNameReclaim, state.FakedContainerName)
	as.NotNil(reclaim)
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 42,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).Difference(reclaim.AllocationResult).String(),
	}, resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName])
	as.NotNil(resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[mainContainerName].ResourceAllocation[string(v1.ResourceCPU)])
	// reserve pool size: 2, reclaimed pool size: 4, share pool size: 42
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 42,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).Difference(reclaim.AllocationResult).String(),
	}, resizeSidecarContainerAllocations.PodResources[podUID].ContainerResources[sidecarContainerName].ResourceAllocation[string(v1.ResourceCPU)])

	mainContainerAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, mainContainerName)
	as.Equal(float64(3), mainContainerAllocation.RequestQuantity)
	sidecarContainerAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, sidecarContainerName)
	as.Equal(float64(2), sidecarContainerAllocation.RequestQuantity)

	// check pod aggregated request
	aggregatedRequest, ok = mainContainerAllocation.GetPodAggregatedRequest()
	as.Equal(true, ok)
	as.Equal(float64(5), aggregatedRequest)
}
