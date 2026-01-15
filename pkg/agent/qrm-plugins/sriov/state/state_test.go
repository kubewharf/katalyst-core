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

package state

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVFState_Sort(t *testing.T) {
	vfState := VFState{
		{RepName: "eth0_0", Index: 0, NumaNode: 0},
		{RepName: "eth0_1", Index: 1, NumaNode: 0},
		{RepName: "eth1_0", Index: 0, NumaNode: 0},
		{RepName: "eth1_1", Index: 1, NumaNode: 0},
		{RepName: "eth2_0", Index: 0, NumaNode: 1},
		{RepName: "eth2_1", Index: 1, NumaNode: 1},
	}

	expected := []string{"eth0_0", "eth1_0", "eth2_0", "eth0_1", "eth1_1", "eth2_1"}

	vfState.Sort()
	got := make([]string, 0, len(vfState))
	for _, i := range vfState {
		got = append(got, i.RepName)
	}

	assert.Equalf(t, expected, got, "vf state sorted in unexpected order")
}
