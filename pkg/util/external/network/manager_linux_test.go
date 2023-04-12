//go:build linux
// +build linux

// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package network

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

var (
	podUID             = "pod-id-1"
	containerID        = "container-id-1"
	classID     uint32 = 1
	cgID        uint64 = 1
)

func TestNewDefaultManager(t *testing.T) {
	defaultManager := NewNetworkManager()
	assert.NotNil(t, defaultManager)

	return
}

func TestApplyNetClass(t *testing.T) {
	defaultManager := NewNetworkManager()
	assert.NotNil(t, defaultManager)

	err := defaultManager.ApplyNetClass(podUID, containerID, &common.NetClsData{
		ClassID:  classID,
		CgroupID: cgID,
	})
	assert.Error(t, err)

	return
}

func TestClearNetClass(t *testing.T) {
	defaultManager := NewNetworkManager()
	assert.NotNil(t, defaultManager)

	err := defaultManager.ClearNetClass(cgID)
	assert.Error(t, err)

	return
}
