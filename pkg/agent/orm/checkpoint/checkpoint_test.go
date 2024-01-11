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

var testCheckpointData = `{"Data":{"PodResourceEntries":[{"PodUID":"447b997f-26a2-4e46-8370-d7af4cc78ad2","ContainerName":"liveness-probe","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"447b997f-26a2-4e46-8370-d7af4cc78ad2","ContainerName":"liveness-probe","ResourceName":"cpu","AllocationInfo":"\n\nCpusetCpus\u0018\u0001!\u0000\u0000\u0000\u0000\u0000\u0000\u001c@*\u00031-7"},{"PodUID":"2df9cabd-5212-4436-a089-89fcab46bfc7","ContainerName":"server","ResourceName":"cpu","AllocationInfo":"\n\nCpusetCpus\u0018\u0001!\u0000\u0000\u0000\u0000\u0000\u0000\u0010@*\u00034-7"},{"PodUID":"2df9cabd-5212-4436-a089-89fcab46bfc7","ContainerName":"server","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"46d9160c-096e-4d63-8b94-766acc9062fc","ContainerName":"kube-flannel","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"0eeeaa35-b954-4498-bf63-6cf6cfbdc620","ContainerName":"server","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"0eeeaa35-b954-4498-bf63-6cf6cfbdc620","ContainerName":"server","ResourceName":"cpu","AllocationInfo":"\n\nCpusetCpus\u0018\u0001!\u0000\u0000\u0000\u0000\u0000\u0000\u0010@*\u00034-7"},{"PodUID":"95df62b2-87a7-49f6-91b7-c801f9630c93","ContainerName":"inspector","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"6d37ac5f-ff2c-4afa-8df7-18cd06ec53a7","ContainerName":"coredns","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"6d37ac5f-ff2c-4afa-8df7-18cd06ec53a7","ContainerName":"coredns","ResourceName":"cpu","AllocationInfo":"\n\nCpusetCpus\u0018\u0001!\u0000\u0000\u0000\u0000\u0000\u0000\u0010@*\u00034-7"},{"PodUID":"185fec2a-69eb-4af3-aa95-967837c5d527","ContainerName":"csi-provisioner","ResourceName":"cpu","AllocationInfo":"\n\nCpusetCpus\u0018\u0001!\u0000\u0000\u0000\u0000\u0000\u0000\u0010@*\u00034-7"},{"PodUID":"185fec2a-69eb-4af3-aa95-967837c5d527","ContainerName":"csi-provisioner","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"185fec2a-69eb-4af3-aa95-967837c5d527","ContainerName":"csi-attacher","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"185fec2a-69eb-4af3-aa95-967837c5d527","ContainerName":"csi-attacher","ResourceName":"cpu","AllocationInfo":"\n\nCpusetCpus\u0018\u0001!\u0000\u0000\u0000\u0000\u0000\u0000\u0010@*\u00034-7"},{"PodUID":"185fec2a-69eb-4af3-aa95-967837c5d527","ContainerName":"liveness-probe","ResourceName":"cpu","AllocationInfo":"\n\nCpusetCpus\u0018\u0001!\u0000\u0000\u0000\u0000\u0000\u0000\u0010@*\u00034-7"},{"PodUID":"185fec2a-69eb-4af3-aa95-967837c5d527","ContainerName":"liveness-probe","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"185fec2a-69eb-4af3-aa95-967837c5d527","ContainerName":"csi-nas-driver","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"185fec2a-69eb-4af3-aa95-967837c5d527","ContainerName":"csi-nas-driver","ResourceName":"cpu","AllocationInfo":"\n\nCpusetCpus\u0018\u0001!\u0000\u0000\u0000\u0000\u0000\u0000\u0010@*\u00034-7"},{"PodUID":"0893ea73-8650-4067-9097-95faf85b2a16","ContainerName":"testcontainer","ResourceName":"cpu","AllocationInfo":"\n\nCpusetCpus\u0018\u0001!\u0000\u0000\u0000\u0000\u0000\u0000\u0010@*\u00034-7"},{"PodUID":"0893ea73-8650-4067-9097-95faf85b2a16","ContainerName":"testcontainer","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"},{"PodUID":"743fb5e0-bba1-47fb-81d6-8e38136b5453","ContainerName":"liveness-probe","ResourceName":"memory","AllocationInfo":"\n\nCpusetMems\u0018\u0001*\u00010"}]},"Checksum":3996144592}`

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
	assert.Equal(t, 21, len(entries))
}
