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

package native

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
)

func TestGenerateURI(t *testing.T) {
	t.Parallel()

	port := 10250
	nodeAddress := "127.0.0.1"
	endpoint := "/configz"

	// ipv4
	uri, err := generateURI(port, nodeAddress, endpoint)
	assert.NoError(t, err)
	assert.Equal(t, "https://127.0.0.1:10250/configz", uri)

	// ipv6
	nodeAddress = "::1"
	uri, err = generateURI(port, nodeAddress, endpoint)
	assert.NoError(t, err)
	assert.Equal(t, "https://[::1]:10250/configz", uri)
}

func TestInsecureConfig(t *testing.T) {
	t.Parallel()

	host := "https://127.0.0.1:10250/configz"
	authTokenFile := "/var/run/secrets/kubernetes.io/serviceaccount/token"

	_, err := insecureConfig(host, authTokenFile)
	assert.Error(t, err)
}

func TestGetAndUnmarshalForHttps(t *testing.T) {
	t.Parallel()

	port := 10250
	nodeAddress := "127.0.0.1"
	endpoint := "/configz"
	authTokenFile := "/var/run/secrets/kubernetes.io/serviceaccount/token"

	type configzWrapper struct {
		ComponentConfig KubeletConfiguration `json:"kubeletconfig"`
	}
	configz := configzWrapper{}

	err := GetAndUnmarshalForHttps(context.TODO(), port, nodeAddress, endpoint, authTokenFile, &configz)
	assert.Error(t, err)
}

type mockCheckpointManager struct {
	getErr error
}

func (m *mockCheckpointManager) CreateCheckpoint(checkpointKey string, cp checkpointmanager.Checkpoint) error {
	return nil
}

func (m *mockCheckpointManager) GetCheckpoint(checkpointKey string, cp checkpointmanager.Checkpoint) error {
	if m.getErr != nil {
		return m.getErr
	}
	return nil
}

func (m *mockCheckpointManager) RemoveCheckpoint(checkpointKey string) error {
	return nil
}

func (m *mockCheckpointManager) ListCheckpoints() ([]string, error) {
	return nil, nil
}

func TestGetKubeletCheckpoint(t *testing.T) {
	t.Parallel()

	t.Run("GetCheckpoint fails", func(t *testing.T) {
		t.Parallel()
		mockCM := &mockCheckpointManager{
			getErr: fmt.Errorf("mock get error"),
		}
		_, err := GetKubeletCheckpoint(mockCM)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "get checkpoint failed")
	})

	t.Run("GetCheckpoint succeeds", func(t *testing.T) {
		t.Parallel()
		mockCM := &mockCheckpointManager{}
		cp, err := GetKubeletCheckpoint(mockCM)
		assert.NoError(t, err)
		assert.NotNil(t, cp)
	})
}
