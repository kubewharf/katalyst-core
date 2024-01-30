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

package checkpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var testCheckpointData = `{"Data":{"PodResourceEntries":[{"PodUID":"62c9e5d2-b2a9-496f-a1eb-ad0f4f02ba5c","ContainerName":"testcontainer","ResourceName":"cpu","AllocationInfo":"{\"oci_property_name\":\"CpusetCpus\",\"is_scalar_resource\":true,\"allocated_quantity\":2,\"allocation_result\":\"0-1\",\"resource_hints\":{\"hints\":[{\"nodes\":[0],\"preferred\":true}]}}"},{"PodUID":"62c9e5d2-b2a9-496f-a1eb-ad0f4f02ba5c","ContainerName":"testcontainer","ResourceName":"memory","AllocationInfo":"{\"oci_property_name\":\"CpusetMems\",\"is_scalar_resource\":true,\"allocated_quantity\":4294967296,\"allocation_result\":\"0\",\"resource_hints\":{\"hints\":[{\"nodes\":[0],\"preferred\":true}]}}"}]},"Checksum":4217255668}`

func TestData_UnmarshalCheckpoint(t *testing.T) {
	t.Parallel()

	entries := make([]PodResourcesEntry, 0)

	data := New(entries)
	err := data.UnmarshalCheckpoint([]byte(testCheckpointData))
	assert.NoError(t, err)

	bytes, err := data.MarshalCheckpoint()
	assert.NoError(t, err)
	assert.Equal(t, []byte(testCheckpointData), bytes)

	err = data.VerifyChecksum()
	assert.NoError(t, err)

	entries = data.GetData()
	assert.Equal(t, 2, len(entries))
}
