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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
)

func Test_secretBackedAccessControl_Verify(t *testing.T) {
	t.Parallel()

	conf := generic.NewAuthConfiguration()
	conf.AccessControlSecretName = "access-control"
	conf.AccessControlSecretNameSpace = "default"

	fakeKubeClient := fake.NewSimpleClientset()
	clientSet := &client.GenericClientSet{
		KubeClient: fakeKubeClient,
	}

	_ = fakeKubeClient.Tracker().Add(&core.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: conf.AccessControlSecretNameSpace,
			Name:      conf.AccessControlSecretName,
		},
		StringData: map[string]string{
			"user-1": PermissionTypeAll,
			"user-2": strings.Join([]string{PermissionTypeHttpEndpoint}, resourceSeparator),
		},
	})

	accessControl, err := NewSecretBackedAccessControl(conf, clientSet)
	assert.NoError(t, err)
	assert.NotNil(t, accessControl)

	secretBackedAC := accessControl.(*secretBackedAccessControl)
	secretBackedAC.updateAuthInfoFromSecret()

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
		t.Run(tt.name, func(t *testing.T) {
			if err := accessControl.Verify(tt.args.authInfo, tt.args.targetResource); (err != nil) != tt.wantErr {
				t.Errorf("Verify() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
