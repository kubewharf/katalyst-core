//go:build !linux
// +build !linux

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

package cgroupid

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

type unsupportedCgroupIDManager struct{}

// NewCgroupIDManager returns a CgroupIDManager
func NewCgroupIDManager(_ pod.PodFetcher) CgroupIDManager {
	return &unsupportedCgroupIDManager{}
}

// Run starts a cgroupIDManagerImpl
func (m *unsupportedCgroupIDManager) Run(_ context.Context) {
}

// GetCgroupIDForContainer returns the cgroup id of a given container.
func (m *unsupportedCgroupIDManager) GetCgroupIDForContainer(podUID, containerID string) (uint64, error) {
	return 0, nil
}

// ListCgroupIDsForPod returns the cgroup ids of a given pod.
func (m *unsupportedCgroupIDManager) ListCgroupIDsForPod(podUID string) ([]uint64, error) {
	return nil, nil
}
