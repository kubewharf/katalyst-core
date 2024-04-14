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

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/credential"
)

func Test_staticAccessControl_Verify(t *testing.T) {
	t.Parallel()

	s := &staticAccessControl{
		SubjectToResources: AuthRule{
			"user-1": []PermissionType{PermissionTypeHttpEndpoint},
		},
	}
	type args struct {
		authInfo       credential.AuthInfo
		targetResource PermissionType
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "user has permission",
			args: args{
				authInfo: credential.BasicAuthInfo{
					Username: "user-1",
				},
				targetResource: PermissionTypeHttpEndpoint,
			},
			wantErr: false,
		},
		{
			name: "user has permission",
			args: args{
				authInfo: credential.BasicAuthInfo{
					Username: "user-1",
				},
				targetResource: PermissionTypeEvictionPlugin,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := s.Verify(tt.args.authInfo, tt.args.targetResource)
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}
