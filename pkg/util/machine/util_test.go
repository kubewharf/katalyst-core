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

package machine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseCPUAssignmentFormat(t *testing.T) {
	t.Parallel()

	assignment := map[int]CPUSet{
		0: NewCPUSet(1, 2),
		1: NewCPUSet(3, 4),
	}
	assert.Equal(t, map[uint64]string{
		0: "1-2",
		1: "3-4",
	}, ParseCPUAssignmentFormat(assignment))
}

func TestDeepcopyCPUAssignment(t *testing.T) {
	t.Parallel()

	assignment := map[int]CPUSet{
		0: NewCPUSet(1, 2),
		1: NewCPUSet(3, 4),
	}
	assert.Equal(t, assignment, DeepcopyCPUAssignment(assignment))
}

func TestMaskToUInt64Array(t *testing.T) {
	t.Parallel()

	mask, err := NewBitMask(0, 1, 2, 3)
	assert.NoError(t, err)
	assert.Equal(t, []uint64{0, 1, 2, 3}, MaskToUInt64Array(mask))
}
