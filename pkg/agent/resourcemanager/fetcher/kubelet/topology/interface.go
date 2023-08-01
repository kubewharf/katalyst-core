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

package topology

import (
	"context"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

// Adapter is to get topology zone status, the src of that can be pod resource api
// or kubelet checkpoint.
type Adapter interface {
	// GetTopologyZones return newest topology zone status
	GetTopologyZones(ctx context.Context) ([]*nodev1alpha1.TopologyZone, error)

	// GetTopologyPolicy return newest topology policy status
	GetTopologyPolicy(ctx context.Context) (nodev1alpha1.TopologyPolicy, error)

	// Run is to start the topology adapter to watch the topology change
	Run(ctx context.Context, handler func()) error
}

// DummyAdapter is a dummy topology adapter for test
type DummyAdapter struct{}

var _ Adapter = DummyAdapter{}

// GetTopologyZones is to get dummy topology zone status
func (d DummyAdapter) GetTopologyZones(_ context.Context) ([]*nodev1alpha1.TopologyZone, error) {
	return []*nodev1alpha1.TopologyZone{}, nil
}

// GetTopologyPolicy is to get dummy topology policy status
func (d DummyAdapter) GetTopologyPolicy(_ context.Context) (nodev1alpha1.TopologyPolicy, error) {
	dummyTopologyPolicy := nodev1alpha1.TopologyPolicy("")
	return dummyTopologyPolicy, nil
}

// Run is to start the dummy topology adapter
func (d DummyAdapter) Run(_ context.Context, _ func()) error {
	return nil
}
