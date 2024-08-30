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

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSNBMemoryVPA(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSNBMemoryVPA")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)
	dynamicPolicy.podAnnotationKeptKeys = []string{
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	testName := "test"

	podUID := string(uuid.NewUUID())
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	res, err := dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	as.NotNil(res)
	as.NotNil(res.ResourceHints[string(v1.ResourceMemory)])
	as.NotZero(len(res.ResourceHints[string(v1.ResourceMemory)].Hints))

	req = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Hint: res.ResourceHints[string(v1.ResourceMemory)].Hints[0],
	}
	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	allocation, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(allocation)
	as.NotNil(allocation.PodResources)
	as.NotNil(allocation.PodResources[req.PodUid])
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources)
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName])
	as.Equal(float64(2147483648), allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	resizeReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 4294967296,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	// resize
	resizeRes, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.Nil(err)
	as.NotNil(resizeRes)
	as.NotNil(resizeRes.ResourceHints[string(v1.ResourceMemory)])
	as.NotZero(len(resizeRes.ResourceHints[string(v1.ResourceMemory)].Hints))

	resizeReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 4294967296,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Hint: resizeRes.ResourceHints[string(v1.ResourceMemory)].Hints[0],
	}
	_, err = dynamicPolicy.Allocate(context.Background(), resizeReq)
	as.Nil(err)

	resizeAllocation, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(resizeAllocation)
	as.NotNil(resizeAllocation.PodResources)
	as.NotNil(resizeAllocation.PodResources[req.PodUid])
	as.NotNil(resizeAllocation.PodResources[req.PodUid].ContainerResources)
	as.NotNil(resizeAllocation.PodResources[req.PodUid].ContainerResources[req.ContainerName])
	as.Equal(float64(4294967296), resizeAllocation.PodResources[req.PodUid].ContainerResources[req.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resize exceed (per numa 7G)
	resizeReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 8 * 1024 * 1024 * 1024,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.NotNil(err)
}

func TestSNBMemoryVPAWithSidecar(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSNBMemoryVPAWithSidecar")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)
	dynamicPolicy.podAnnotationKeptKeys = []string{
		consts.PodAnnotationMemoryEnhancementNumaBinding,
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	testName := "test"
	sidecarName := "sidecar"

	// allocate sidecar firstly
	podUID := string(uuid.NewUUID())
	sidecarReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"memory": 4294967296}`, // sidecar 2G + main 2G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	// no topology hints for snb sidecar
	res, err := dynamicPolicy.GetTopologyHints(context.Background(), sidecarReq)
	as.Nil(err)
	as.NotNil(res)
	as.Nil(res.ResourceHints[string(v1.ResourceMemory)])

	// no allocation info for snb sidecar
	sidecarReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"memory": 4294967296}`, // sidecar 2G + main 2G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}
	allocationRes, err := dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)
	as.Nil(allocationRes.AllocationResult)

	// allocate main container
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"memory": 4294967296}`, // sidecar 2G + main 2G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	res, err = dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	as.NotNil(res)
	as.NotNil(res.ResourceHints[string(v1.ResourceMemory)])
	as.NotZero(len(res.ResourceHints[string(v1.ResourceMemory)].Hints))

	req = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"memory": 4294967296}`, // sidecar 2G + main 2G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Hint: res.ResourceHints[string(v1.ResourceMemory)].Hints[0],
	}
	allocationRes, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)
	as.NotNil(allocationRes.AllocationResult)

	allocation, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(allocation)
	as.NotNil(allocation.PodResources)
	as.NotNil(allocation.PodResources[req.PodUid])
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources)
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName])
	// allocate all resource into main container: sidecar 2G + main 2G
	as.Equal(float64(4294967296), allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// reallocate sidecar here
	sidecarReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"memory": 4294967296}`, // sidecar 2G + main 2G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}
	_, err = dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)

	allocation, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(allocation)
	as.NotNil(allocation.PodResources)
	as.NotNil(allocation.PodResources[sidecarReq.PodUid])
	as.NotNil(allocation.PodResources[sidecarReq.PodUid].ContainerResources)
	as.NotNil(allocation.PodResources[sidecarReq.PodUid].ContainerResources[sidecarReq.ContainerName])
	// allocate all resource into main container, here is zero
	as.Equal(float64(0), allocation.PodResources[sidecarReq.PodUid].ContainerResources[sidecarReq.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resize main container
	resizeReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 4294967296,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey:    `{"memory": 6442450944}`, // sidecar 2G + main 4G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	resizeRes, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.Nil(err)
	as.NotNil(resizeRes)
	as.NotNil(resizeRes.ResourceHints[string(v1.ResourceMemory)])
	as.NotZero(len(resizeRes.ResourceHints[string(v1.ResourceMemory)].Hints))

	resizeReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 4294967296,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey:    `{"memory": 6442450944}`, // sidecar 2G + main 4G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Hint: resizeRes.ResourceHints[string(v1.ResourceMemory)].Hints[0],
	}
	_, err = dynamicPolicy.Allocate(context.Background(), resizeReq)
	as.Nil(err)

	resizeAllocation, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(resizeAllocation)
	as.NotNil(resizeAllocation.PodResources)
	as.NotNil(resizeAllocation.PodResources[req.PodUid])
	as.NotNil(resizeAllocation.PodResources[req.PodUid].ContainerResources)
	as.NotNil(resizeAllocation.PodResources[req.PodUid].ContainerResources[req.ContainerName])
	// allocate all resource into main container: sidecar 2G + main 4G
	as.Equal(float64(6442450944), resizeAllocation.PodResources[req.PodUid].ContainerResources[req.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)
	as.NotNil(allocation.PodResources[sidecarReq.PodUid].ContainerResources[sidecarReq.ContainerName])
	// allocate all resource into main container, here is zero
	as.Equal(float64(0), allocation.PodResources[sidecarReq.PodUid].ContainerResources[sidecarReq.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resize sidecar container
	resizeSidecarReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 3 * 1024 * 1024 * 1024,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey:    `{"memory": 7516192768}`, // sidecar 3G + main 4G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	resizeSidecarRes, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeSidecarReq)
	as.Nil(err)
	as.NotNil(resizeSidecarRes)
	// no topology hints for snb sidecar
	as.Nil(resizeSidecarRes.ResourceHints[string(v1.ResourceMemory)])

	resizeSidecarReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 3 * 1024 * 1024 * 1024,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey:    `{"memory": 7516192768}`, // sidecar 3G + main 4G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}
	_, err = dynamicPolicy.Allocate(context.Background(), resizeSidecarReq)
	as.Nil(err)

	// resize main container again (no change)
	resizeReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 4294967296,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey:    `{"memory": 7516192768}`, // sidecar 2G + main 4G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Hint: resizeRes.ResourceHints[string(v1.ResourceMemory)].Hints[0],
	}
	resizeRes, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.Nil(err)
	as.NotNil(resizeRes)
	as.NotNil(resizeRes.ResourceHints[string(v1.ResourceMemory)])
	as.NotZero(len(resizeRes.ResourceHints[string(v1.ResourceMemory)].Hints))

	resizeReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 4294967296,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey:    `{"memory": 7516192768}`, // sidecar 2G + main 4G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Hint: resizeRes.ResourceHints[string(v1.ResourceMemory)].Hints[0],
	}
	_, err = dynamicPolicy.Allocate(context.Background(), resizeReq)
	as.Nil(err)

	resizeAllocation, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(resizeAllocation)
	as.NotNil(resizeAllocation.PodResources)
	as.NotNil(resizeAllocation.PodResources[resizeSidecarReq.PodUid])
	as.NotNil(resizeAllocation.PodResources[resizeSidecarReq.PodUid].ContainerResources)
	as.NotNil(resizeAllocation.PodResources[resizeSidecarReq.PodUid].ContainerResources[testName])
	// allocate all resource into main container: sidecar 3G + main 4G
	as.Equal(float64(7516192768), resizeAllocation.PodResources[resizeSidecarReq.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)
	as.NotNil(allocation.PodResources[resizeSidecarReq.PodUid].ContainerResources[resizeSidecarReq.ContainerName])
	// allocate all resource into main container, here is zero
	as.Equal(float64(0), allocation.PodResources[resizeSidecarReq.PodUid].ContainerResources[resizeSidecarReq.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resize main exceeded
	resizeReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 32 * 1024 * 1024 * 1024,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey:    `{"memory": 37580963840}`, // sidecar 2G + main 4G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Hint: resizeRes.ResourceHints[string(v1.ResourceMemory)].Hints[0],
	}
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.NotNil(err)
	// resize sidecar exceeded
	resizeSidecarReq = &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 32 * 1024 * 1024 * 1024,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey:    `{"memory": 37580963840}`, // sidecar 3G + main 4G
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeSidecarReq)
	// TODO expected error here, but now not check sidecar
	// it is not important to check sidecar in hints, because kubelet only patch pod spec when all container admit successfully
	as.Nil(err)
}

func TestNormalShareMemoryVPA(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestNormalShareMemoryVPA")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)
	dynamicPolicy.podAnnotationKeptKeys = []string{
		consts.PodAnnotationMemoryEnhancementNumaBinding,
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	testName := "test"

	podUID := string(uuid.NewUUID())
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	// no hints for normal share cores container
	res, err := dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	as.NotNil(res)
	as.Nil(res.ResourceHints[string(v1.ResourceMemory)])

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	allocation, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(allocation)
	as.NotNil(allocation.PodResources)
	as.NotNil(allocation.PodResources[req.PodUid])
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources)
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName])
	as.Equal(float64(2147483648), allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resize the container
	resizeReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 4294967296,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	// no hints for normal share cores container
	resizeRes, err := dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.Nil(err)
	as.NotNil(resizeRes)
	as.Nil(resizeRes.ResourceHints[string(v1.ResourceMemory)])

	_, err = dynamicPolicy.Allocate(context.Background(), resizeReq)
	as.Nil(err)

	allocation, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(allocation)
	as.NotNil(allocation.PodResources)
	as.NotNil(allocation.PodResources[resizeReq.PodUid])
	as.NotNil(allocation.PodResources[resizeReq.PodUid].ContainerResources)
	as.NotNil(allocation.PodResources[resizeReq.PodUid].ContainerResources[req.ContainerName])
	as.Equal(float64(4294967296), allocation.PodResources[resizeReq.PodUid].ContainerResources[resizeReq.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resie exceeded
	// no hints for normal share cores container
	resizeReq.Annotations[consts.PodAnnotationAggregatedRequestsKey] = `{"memory": 35433480192}`
	resizeReq.ResourceRequests[string(v1.ResourceMemory)] = 33 * 1024 * 1024 * 1024
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.NotNil(err)
}

func TestNormalShareMemoryVPAWithSidecar(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestNormalShareMemoryVPAWithSidecar")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)
	dynamicPolicy.podAnnotationKeptKeys = []string{
		consts.PodAnnotationMemoryEnhancementNumaBinding,
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	testName := "test"
	sidecarName := "sidecar"
	// allocate main container
	podUID := string(uuid.NewUUID())
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	// no hints for normal share cores container
	res, err := dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	as.NotNil(res)
	as.Nil(res.ResourceHints[string(v1.ResourceMemory)])

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	allocation, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(allocation)
	as.NotNil(allocation.PodResources)
	as.NotNil(allocation.PodResources[req.PodUid])
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources)
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName])
	as.Equal(float64(2147483648), allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// allocate sidecar container
	sidecarReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	// no hints for normal share cores container
	res, err = dynamicPolicy.GetTopologyHints(context.Background(), sidecarReq)
	as.Nil(err)
	as.NotNil(res)
	as.Nil(res.ResourceHints[string(v1.ResourceMemory)])

	_, err = dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)

	allocation, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(allocation)
	as.NotNil(allocation.PodResources)
	as.NotNil(allocation.PodResources[sidecarReq.PodUid])
	as.NotNil(allocation.PodResources[sidecarReq.PodUid].ContainerResources)
	as.NotNil(allocation.PodResources[sidecarReq.PodUid].ContainerResources[sidecarReq.ContainerName])
	as.Equal(float64(2147483648), allocation.PodResources[sidecarReq.PodUid].ContainerResources[sidecarReq.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resize main container
	resizeMainReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 4294967296,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	// no hints for normal share cores container
	res, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeMainReq)
	as.Nil(err)
	as.NotNil(res)
	as.Nil(res.ResourceHints[string(v1.ResourceMemory)])

	_, err = dynamicPolicy.Allocate(context.Background(), resizeMainReq)
	as.Nil(err)

	allocation, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(allocation)
	as.NotNil(allocation.PodResources)
	as.NotNil(allocation.PodResources[req.PodUid])
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources)
	as.NotNil(allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName])
	as.Equal(float64(4294967296), allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resize sidecar container
	sidecarResizeReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 3221225472,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	// no hints for normal share cores container
	res, err = dynamicPolicy.GetTopologyHints(context.Background(), sidecarResizeReq)
	as.Nil(err)
	as.NotNil(res)
	as.Nil(res.ResourceHints[string(v1.ResourceMemory)])

	_, err = dynamicPolicy.Allocate(context.Background(), sidecarResizeReq)
	as.Nil(err)

	allocation, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(allocation)
	as.NotNil(allocation.PodResources)
	as.NotNil(allocation.PodResources[sidecarReq.PodUid])
	as.NotNil(allocation.PodResources[sidecarReq.PodUid].ContainerResources)
	as.NotNil(allocation.PodResources[sidecarReq.PodUid].ContainerResources[sidecarReq.ContainerName])
	as.Equal(float64(3221225472), allocation.PodResources[sidecarReq.PodUid].ContainerResources[sidecarReq.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resize exceeded
	resizeMainReq.ResourceRequests[string(v1.ResourceMemory)] = 35433480192
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeMainReq)
	as.NotNil(err)
}
