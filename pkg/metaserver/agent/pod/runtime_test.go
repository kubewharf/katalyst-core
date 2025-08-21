package pod

import (
	"github.com/stretchr/testify/assert"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	critesting "k8s.io/cri-api/pkg/apis/testing"
	"testing"
)

func TestRuntimePodFetcherImpl_GetContainerInfo(t *testing.T) {
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
