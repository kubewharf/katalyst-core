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

package orm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/checkpoint"
)

func TestPodResources(t *testing.T) {
	t.Parallel()

	podResource := newPodResourcesChk()

	resourceAllocationInfo := generateResourceAllocationInfo()

	podResource.insert("testPod", "testContainer", "cpu", resourceAllocationInfo)

	containerResources := podResource.podResources("testPod")
	assert.NotNil(t, containerResources)
	assert.Equal(t, len(containerResources), 1)
	containerResources = podResource.podResources("nonPod")
	assert.Nil(t, containerResources)

	containerAllResources := podResource.containerAllResources("testPod", "testContainer")
	assert.NotNil(t, containerAllResources)
	assert.Equal(t, len(containerAllResources), 1)
	containerAllResources = podResource.containerAllResources("nonPod", "testContainer")
	assert.Nil(t, containerAllResources)
	containerAllResources = podResource.containerAllResources("testPod", "nonContainer")
	assert.Nil(t, containerAllResources)

	podSet := podResource.pods()
	assert.Equal(t, podSet, sets.NewString("testPod"))
	resourceSet := podResource.allAllocatedResourceNames()
	assert.Equal(t, resourceSet, sets.NewString("cpu"))

	podResource.insert("testPod", "testContainer", "memory", resourceAllocationInfo)
	podResource.insert("testPod2", "testContainer2", "cpu", resourceAllocationInfo)
	entries := podResource.toCheckpointData()
	assert.Equal(t, len(entries), 3)

	podResource.deletePod("testPod")
	podResource.deletePod("testPod2")
	containerResources = podResource.podResources("testPod")
	assert.Nil(t, containerResources)

	podResource.insert("testPod", "testContainer", "cpu", resourceAllocationInfo)
	podResource.delete([]string{"testPod"})
	containerResources = podResource.podResources("testPod")
	assert.Nil(t, containerResources)

	podResource.insert("testPod", "testContainer", "cpu", resourceAllocationInfo)
	podResource.deleteResourceAllocationInfo("testPod", "testContainer", "cpu")
	containerAllResources = podResource.containerAllResources("testPod", "testContainer")
	assert.NotNil(t, containerAllResources)
	assert.Equal(t, len(containerAllResources), 0)
}

func TestCheckpointMarshal(t *testing.T) {
	t.Parallel()
	/* ----  use allocationInfo.Marshal  ----- */
	podResources := newPodResourcesChk()
	podResources.resources = map[string]ContainerResources{
		"testPodUid": map[string]ResourceAllocation{
			"testContainer": map[string]*pluginapi.ResourceAllocationInfo{
				"memory": {
					OciPropertyName:   "CpusetMems",
					IsNodeResource:    true,
					IsScalarResource:  true,
					AllocatedQuantity: 400000000, // more than 1000
					AllocationResult:  "0",
				},
			},
		},
	}

	var entries []checkpoint.PodResourcesEntry
	for podUID, containerResources := range podResources.resources {
		for conName, resourcesAllocation := range containerResources {
			for resourceName, allocationInfo := range resourcesAllocation {
				// use allocationInfo.Marshal()
				allocRespBytes, err := allocationInfo.Marshal()
				if err != nil {
					klog.Errorf("Can't marshal allocationInfo for %v %v %v: %v", podUID, conName, resourceName, err)
					continue
				}
				entries = append(entries, checkpoint.PodResourcesEntry{
					PodUID:         podUID,
					ContainerName:  conName,
					ResourceName:   resourceName,
					AllocationInfo: string(allocRespBytes),
				})
			}
		}
	}
	data := checkpoint.New(entries)
	value, err := data.MarshalCheckpoint()
	assert.NoError(t, err)

	resEntries := make([]checkpoint.PodResourcesEntry, 0)
	cp := checkpoint.New(resEntries)
	err = cp.UnmarshalCheckpoint(value)
	assert.NoError(t, err)
	// we just marshal and unmarshal the data, checksum should be the same.
	err = cp.VerifyChecksum()
	assert.Error(t, err) // here we got an error, actually we can not get checkpoint data from file.

	/* ----  use json.Marshal  ----- */
	podResources = newPodResourcesChk()
	podResources.resources = map[string]ContainerResources{
		"testPodUid": map[string]ResourceAllocation{
			"testContainer": map[string]*pluginapi.ResourceAllocationInfo{
				"memory": {
					OciPropertyName:   "CpusetMems",
					IsNodeResource:    true,
					IsScalarResource:  true,
					AllocatedQuantity: 400000000, // more than 1000
					AllocationResult:  "0",
				},
			},
		},
	}

	data = checkpoint.New(podResources.toCheckpointData()) // the only difference is than we use json.Marshal
	value, err = data.MarshalCheckpoint()
	assert.NoError(t, err)

	resEntries = make([]checkpoint.PodResourcesEntry, 0)
	cp = checkpoint.New(resEntries)
	err = cp.UnmarshalCheckpoint(value)
	assert.NoError(t, err)
	// we just marshal and unmarshal the data, checksum should be the same.
	err = cp.VerifyChecksum()
	assert.NoError(t, err)
}

func generateResourceAllocationInfo() *pluginapi.ResourceAllocationInfo {
	return &pluginapi.ResourceAllocationInfo{
		OciPropertyName:   "CpusetCpus",
		IsNodeResource:    true,
		IsScalarResource:  true,
		AllocatedQuantity: 3,
		AllocationResult:  "5-6,10",
		Envs:              map[string]string{"mock_key": "mock_env"},
		Annotations:       map[string]string{"mock_key": "mock_ano"},
		ResourceHints:     &pluginapi.ListOfTopologyHints{},
	}
}
