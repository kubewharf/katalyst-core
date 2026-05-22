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

package resourcepool

import (
	"testing"

	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestGetResourcePoolName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{
			name: "normal case with resource pool annotation",
			annotations: map[string]string{
				consts.PodAnnotationResourcePoolKey: "test-resource-pool",
			},
			want: "test-resource-pool",
		},
		{
			name:        "empty annotations map",
			annotations: map[string]string{},
			want:        "",
		},
		{
			name:        "nil annotations map",
			annotations: nil,
			want:        "",
		},
		{
			name: "annotations map without resource pool key",
			annotations: map[string]string{
				"other-key": "other-value",
			},
			want: "",
		},
		{
			name: "resource pool key with empty value",
			annotations: map[string]string{
				consts.PodAnnotationResourcePoolKey: "",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GetResourcePoolName(tt.annotations); got != tt.want {
				t.Errorf("GetResourcePoolName() = %v, want %v", got, tt.want)
			}
		})
	}
}
