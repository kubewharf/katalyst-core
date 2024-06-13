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

package test

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/msr"
)

func TestMSRDev_Close(t *testing.T) {
	t.Parallel()
	testMSRDev := msr.MSRDev{}
	if err := testMSRDev.Close(); err != nil {
		t.Errorf("expcted no error, got %#v", err)
	}
}

func TestMSR(t *testing.T) {
	t.Parallel()
	_, err := msr.MSR(9)
	if err != nil {
		t.Errorf("unexpected error: %#v", err)
	}
}
