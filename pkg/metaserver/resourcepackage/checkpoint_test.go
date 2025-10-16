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

package resourcepackage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

func TestNewCheckpoint(t *testing.T) {
	t.Parallel()

	now := metav1.Now()

	// Prepare a test NodeProfileDescriptor
	npd := &nodev1alpha1.NodeProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-a",
			Namespace: "default",
		},
		Spec: nodev1alpha1.NodeProfileDescriptorSpec{},
		Status: nodev1alpha1.NodeProfileDescriptorStatus{
			NodeMetrics: []nodev1alpha1.ScopedNodeMetrics{
				{
					Scope: "resource-package",
				},
			},
		},
	}

	// Create a new checkpoint and set data
	cp := NewCheckpoint(NodeProfileData{})
	cp.SetProfile(npd, now)

	// Marshal the checkpoint
	checkpointBytes, err := cp.MarshalCheckpoint()
	assert.NoError(t, err, "should marshal checkpoint successfully")

	// Unmarshal into a new checkpoint object
	loaded := &Data{Item: &DataItem{}}
	err = loaded.UnmarshalCheckpoint(checkpointBytes)
	assert.NoError(t, err, "should unmarshal checkpoint successfully")

	// Verify checksum integrity
	err = loaded.VerifyChecksum()
	assert.NoError(t, err, "checksum verification should pass")

	// Retrieve stored profile and timestamp
	restoredProfile, ts := loaded.GetProfile()

	assert.Equal(t, metav1.Unix(now.Unix(), 0), ts, "timestamp should match")
	assert.Equal(t, npd, restoredProfile, "restored profile should match original")
}
