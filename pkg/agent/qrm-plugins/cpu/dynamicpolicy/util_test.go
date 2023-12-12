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

package dynamicpolicy

import (
	"testing"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
)

func Test_updateAllocationInfoByReq(t *testing.T) {
	t.Parallel()

	type args struct {
		req            *pluginapi.ResourceRequest
		allocationInfo *state.AllocationInfo
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "req == nil",
			args: args{
				allocationInfo: &state.AllocationInfo{},
			},
			wantErr: true,
		},
		{
			name: "update qos level",
			args: args{
				req: &pluginapi.ResourceRequest{
					Annotations: map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
				},
				allocationInfo: &state.AllocationInfo{
					QoSLevel: apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := updateAllocationInfoByReq(tt.args.req, tt.args.allocationInfo); (err != nil) != tt.wantErr {
				t.Errorf("updateAllocationInfoByReq() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
