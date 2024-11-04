//go:build linux
// +build linux

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

package manager

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/bitmask"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestMemoryPolicy(t *testing.T) {
	t.Parallel()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mode, mask, err := GetMemPolicy(nil, 0)
	assert.NoError(t, err)

	assert.Equal(t, MPOL_DEFAULT, mode)

	t.Logf("mask: %v", mask)

	mems := machine.NewCPUSet(0)
	newMask := bitmask.NewEmptyBitMask()
	newMask.Add(mems.ToSliceInt()...)

	err = SetMemPolicy(MPOL_BIND, newMask)
	assert.NoError(t, err)

	mode, mask, err = GetMemPolicy(nil, 0)
	assert.NoError(t, err)

	assert.Equal(t, MPOL_BIND, mode)

	expectMask, err := bitmask.NewBitMask(0)
	assert.NoError(t, err)

	assert.Equal(t, expectMask, mask)

	t.Logf("mask: %v", mask)
}
