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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
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

func TestSNBMemoryVPAWithZeroRequest(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSNBMemoryVPAWithZeroRequest")
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
	// first init dynamic policy with pod zero mem request
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 0,
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
			string(v1.ResourceMemory): 0,
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
	as.Equal(float64(0), allocation.PodResources[req.PodUid].ContainerResources[req.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)

	// resize to 2G
	resizeReq := &pluginapi.ResourceRequest{
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
			consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationInplaceUpdateResizingKey: "true",
			consts.PodAnnotationMemoryEnhancementKey:     `{"numa_binding": "true", "numa_exclusive": "false"}`,
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

	// allocate
	resizeReq = &pluginapi.ResourceRequest{
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
	as.Equal(float64(2147483648), resizeAllocation.PodResources[req.PodUid].ContainerResources[req.ContainerName].ResourceAllocation[string(v1.ResourceMemory)].AllocatedQuantity)
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

func TestNonBindingShareCoresMemoryVPA(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestNonBindingShareCoresMemoryVPA")
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

	// no hints for non-binding share cores container
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

	// no hints for non-binding share cores container
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
	// no hints for non-binding share cores container
	resizeReq.Annotations[consts.PodAnnotationAggregatedRequestsKey] = `{"memory": 35433480192}`
	resizeReq.ResourceRequests[string(v1.ResourceMemory)] = 33 * 1024 * 1024 * 1024
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), resizeReq)
	as.NotNil(err)
}

func TestNonBindingShareCoresMemoryVPAWithSidecar(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestNonBindingShareCoresMemoryVPAWithSidecar")
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

	// no hints for non-binding share cores container
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

	// no hints for non-binding share cores container
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

	// no hints for non-binding share cores container
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

	// no hints for non-binding share cores container
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

func TestRNBMemoryVPA(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	testName := "test"
	podUid := string(uuid.NewUUID())
	testCases := []struct {
		name                   string
		numaHeadroom           map[int]int64
		PodEntries             state.PodEntries
		requestQuantity        float64
		expectedHintErr        bool
		expectedHintResp       *pluginapi.ResourceHintsResponse
		expectedAllocationErr  bool
		expectedAllocationResp *pluginapi.ResourceAllocationResponse
	}{
		{
			name: "non-rnb scale out",
			numaHeadroom: map[int]int64{
				0: 3221225472,
				1: 3221225472,
				2: 3221225472,
				3: 3221225472,
			},
			PodEntries: state.PodEntries{
				podUid: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         podUid,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							QoSLevel:       consts.PodAnnotationQoSLevelReclaimedCores,
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
						},
						AggregatedQuantity:   2147483648,
						NumaAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
					},
				},
			},
			requestQuantity: 3221225472,
			expectedHintErr: false,
			expectedHintResp: &pluginapi.ResourceHintsResponse{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): nil,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationInplaceUpdateResizingKey: "true",
				},
			},
			expectedAllocationErr: false,
			expectedAllocationResp: &pluginapi.ResourceAllocationResponse{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3221225472,
							AllocationResult:  machine.NewCPUSet(0, 1, 2, 3).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationInplaceUpdateResizingKey: "true",
				},
			},
		},
		{
			name: "rnb scale in",
			numaHeadroom: map[int]int64{
				0: 3221225472,
				1: 3221225472,
				2: 3221225472,
				3: 3221225472,
			},
			PodEntries: state.PodEntries{
				podUid: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         podUid,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							QoSLevel:       consts.PodAnnotationQoSLevelReclaimedCores,
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
						},
						AggregatedQuantity:   2147483648,
						NumaAllocationResult: machine.NewCPUSet(0),
					},
				},
			},
			requestQuantity: 1073741824,
			expectedHintErr: false,
			expectedHintResp: &pluginapi.ResourceHintsResponse{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationInplaceUpdateResizingKey:     "true",
				},
			},
			expectedAllocationErr: false,
			expectedAllocationResp: &pluginapi.ResourceAllocationResponse{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 1073741824,
							AllocationResult:  machine.NewCPUSet(0).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								coreconsts.QRMResourceAnnotationKeyNUMABindResult: "0",
								coreconsts.QRMPodAnnotationTopologyAllocationKey:  `{"Numa":{"0":{}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationInplaceUpdateResizingKey:     "true",
				},
			},
		},
		{
			name: "rnb scale out with enough resource",
			numaHeadroom: map[int]int64{
				0: 3221225472,
				1: 3221225472,
				2: 3221225472,
				3: 3221225472,
			},
			PodEntries: state.PodEntries{
				podUid: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         podUid,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							QoSLevel:       consts.PodAnnotationQoSLevelReclaimedCores,
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
						},
						AggregatedQuantity:   2147483648,
						NumaAllocationResult: machine.NewCPUSet(0),
					},
				},
			},
			requestQuantity: 3221225472,
			expectedHintErr: false,
			expectedHintResp: &pluginapi.ResourceHintsResponse{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationInplaceUpdateResizingKey:     "true",
				},
			},
			expectedAllocationErr: false,
			expectedAllocationResp: &pluginapi.ResourceAllocationResponse{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3221225472,
							AllocationResult:  machine.NewCPUSet(0).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								coreconsts.QRMResourceAnnotationKeyNUMABindResult: "0",
								coreconsts.QRMPodAnnotationTopologyAllocationKey:  `{"Numa":{"0":{}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationInplaceUpdateResizingKey:     "true",
				},
			},
		},
		{
			name: "rnb scale out with no enough resource",
			numaHeadroom: map[int]int64{
				0: 3221225472,
				1: 3221225472,
				2: 3221225472,
				3: 3221225472,
			},
			PodEntries: state.PodEntries{
				podUid: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         podUid,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							QoSLevel:       consts.PodAnnotationQoSLevelReclaimedCores,
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
						},
						AggregatedQuantity:   2147483648,
						NumaAllocationResult: machine.NewCPUSet(0),
					},
				},
			},
			requestQuantity: 4294967296,
			expectedHintErr: true,
		},
		{
			name: "rnb non-actual binding scale out with enough resource",
			numaHeadroom: map[int]int64{
				0: 3221225472,
				1: 3221225472,
				2: 3221225472,
				3: 3221225472,
			},
			PodEntries: state.PodEntries{
				podUid: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         podUid,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							QoSLevel:       consts.PodAnnotationQoSLevelReclaimedCores,
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
						},
						AggregatedQuantity:   2147483648,
						NumaAllocationResult: machine.NewCPUSet(0, 1),
					},
				},
			},
			requestQuantity: 6442450944,
			expectedHintErr: false,
			expectedHintResp: &pluginapi.ResourceHintsResponse{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationInplaceUpdateResizingKey:     "true",
				},
			},
			expectedAllocationErr: false,
			expectedAllocationResp: &pluginapi.ResourceAllocationResponse{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 6442450944,
							AllocationResult:  machine.NewCPUSet(0, 1).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0, 1},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								coreconsts.QRMPodAnnotationTopologyAllocationKey: `{"Numa":{"0":{},"1":{}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationInplaceUpdateResizingKey:     "true",
				},
			},
		},
		{
			name: "rnb non-actual binding scale out with no enough resource",
			numaHeadroom: map[int]int64{
				0: 3221225472,
				1: 3221225472,
				2: 3221225472,
				3: 3221225472,
			},
			PodEntries: state.PodEntries{
				podUid: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         podUid,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							QoSLevel:       consts.PodAnnotationQoSLevelReclaimedCores,
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
						},
						AggregatedQuantity:   2147483648,
						NumaAllocationResult: machine.NewCPUSet(0, 1),
					},
				},
			},
			requestQuantity: 8589934592,
			expectedHintErr: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyHints")
			as.Nil(err)

			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			as.Nil(err)
			dynamicPolicy.podAnnotationKeptKeys = []string{consts.PodAnnotationInplaceUpdateResizingKey}

			if tc.numaHeadroom != nil {
				dynamicPolicy.state.SetNUMAHeadroom(tc.numaHeadroom, true)
			}

			if tc.PodEntries != nil {
				podResourceEntries := map[v1.ResourceName]state.PodEntries{v1.ResourceMemory: tc.PodEntries}
				machineState, err := state.GenerateMachineStateFromPodEntries(machineInfo, nil, podResourceEntries, nil,
					dynamicPolicy.state.GetReservedMemory(), nil)
				as.Nil(err)

				dynamicPolicy.state.SetMachineState(machineState, true)
				dynamicPolicy.state.SetPodResourceEntries(podResourceEntries, true)

			}

			numaBinding := false
			allocationInfo := dynamicPolicy.state.GetAllocationInfo(v1.ResourceMemory, podUid, testName)
			as.NotNil(allocationInfo)
			if allocationInfo.CheckNUMABinding() {
				numaBinding = true
			}

			hintReq := &pluginapi.ResourceRequest{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): tc.requestQuantity,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationInplaceUpdateResizingKey: "true",
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			}

			if numaBinding {
				hintReq.Annotations[consts.PodAnnotationMemoryEnhancementKey] = `{"numa_binding": "true"}`
			}

			hintResp, err := dynamicPolicy.GetTopologyHints(context.Background(), hintReq)
			as.Equalf(err != nil, tc.expectedHintErr, "expected hint error: %v, got: %v", tc.expectedHintErr, err)
			as.Equal(tc.expectedHintResp, hintResp, "got unexpected hint response")

			if tc.expectedHintErr {
				return
			}

			allocationReq := &pluginapi.ResourceRequest{
				PodUid:         podUid,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): tc.requestQuantity,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationInplaceUpdateResizingKey: "true",
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			}

			if numaBinding {
				as.Equalf(len(hintResp.ResourceHints[string(v1.ResourceMemory)].Hints), 1, "expected 1 hints, got: %v", hintResp.ResourceHints)

				allocationReq.Hint = hintResp.ResourceHints[string(v1.ResourceMemory)].Hints[0]
				allocationReq.Annotations[consts.PodAnnotationMemoryEnhancementKey] = `{"numa_binding": "true"}`
			}

			allocationResp, err := dynamicPolicy.Allocate(context.Background(), allocationReq)
			as.Equalf(err != nil, tc.expectedAllocationErr, "expected allocation error: %v, got: %v", tc.expectedAllocationErr, err)
			as.Equal(tc.expectedAllocationResp, allocationResp, "got unexpected allocation response")
		})
	}
}
