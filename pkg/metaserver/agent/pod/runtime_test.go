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

package pod

import (
	"testing"

	"github.com/stretchr/testify/assert"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	critesting "k8s.io/cri-api/pkg/apis/testing"
)

func TestRuntimePodFetcherImpl_GetContainerInfo(t *testing.T) {
	t.Parallel()

	fakeRuntimeService := critesting.NewFakeRuntimeService()

	// Add a fake container to the fake runtime
	fakeRuntimeService.Containers["fakeContainerID"] = &critesting.FakeContainer{
		ContainerStatus: runtimeapi.ContainerStatus{
			Id: "fakeContainerID",
		},
	}

	fakeRuntimePodFetcher := &runtimePodFetcherImpl{
		runtimeService: fakeRuntimeService,
	}

	// Should throw an error because the containerID is invalid
	resp, err := fakeRuntimePodFetcher.GetContainerInfo("invalidContainerID")
	assert.Nil(t, resp)
	assert.Error(t, err)

	// Should throw an error because the containerStatus has no info
	resp, err = fakeRuntimePodFetcher.GetContainerInfo("fakeContainerID")
	assert.Nil(t, resp)
	assert.Error(t, err)
}
