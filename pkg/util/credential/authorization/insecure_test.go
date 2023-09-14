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

package authorization

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/credential"
)

func Test_insecureAccessControl_Verify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		authInfo   credential.AuthInfo
		permission PermissionType
		wantErr    bool
	}{
		{
			name:       "http permission",
			authInfo:   &credential.AnonymousAuthInfo{},
			permission: PermissionTypeHttpEndpoint,
			wantErr:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := &insecureAccessControl{}
			if err := i.Verify(tt.authInfo, tt.permission); (err != nil) != tt.wantErr {
				t.Errorf("Verify() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
