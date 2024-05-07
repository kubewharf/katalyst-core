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

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
)

func Test_dynamicConfAccessControl_Verify(t *testing.T) {
	t.Parallel()

	conf := generic.NewAuthConfiguration()

	configuration := dynamic.NewDynamicAgentConfiguration()
	dynamicConf := dynamic.NewConfiguration()
	dynamicConf.AccessControlPolicies = []v1alpha1.AccessControlPolicy{
		{
			Username: "user-1",
			PolicyRule: v1alpha1.PolicyRule{
				Resources: []string{PermissionTypeAll},
			},
		},
		{
			Username: "user-2",
			PolicyRule: v1alpha1.PolicyRule{
				Resources: []string{PermissionTypeHttpEndpoint},
			},
		},
	}
	configuration.SetDynamicConfiguration(dynamicConf)

	accessControl, err := NewDynamicConfAccessControl(conf, configuration)
	assert.NoError(t, err)
	assert.NotNil(t, accessControl)

	secretBackedAC := accessControl.(*dynamicConfAccessControl)
	secretBackedAC.updateAuthInfoFromDynamicConf()

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
			name: "invalid user",
			args: args{
				authInfo: credential.BasicAuthInfo{
					Username: "user-3",
				},
				targetResource: PermissionTypeHttpEndpoint,
			},
			wantErr: true,
		},
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
			name: "user has no permission",
			args: args{
				authInfo: credential.BasicAuthInfo{
					Username: "user-2",
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
			if err := accessControl.Verify(tt.args.authInfo, tt.args.targetResource); (err != nil) != tt.wantErr {
				t.Errorf("Verify() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
