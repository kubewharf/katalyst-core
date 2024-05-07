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

package credential

import (
	"encoding/base64"
	"net/http"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func makeBasicToken(user, password string) string {
	rawString := user + ":" + password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(rawString))
}

func makeCred(t *testing.T) Credential {
	conf := generic.NewAuthConfiguration()

	configuration := dynamic.NewDynamicAgentConfiguration()
	dynamicConf := dynamic.NewConfiguration()
	dynamicConf.UserPasswordPairs = []v1alpha1.UserPasswordPair{
		{Username: "user-1", Password: base64.StdEncoding.EncodeToString([]byte("123456"))},
		{Username: "user-2", Password: base64.StdEncoding.EncodeToString([]byte("abcdefg"))},
	}
	configuration.SetDynamicConfiguration(dynamicConf)

	credential, err := NewBasicAuthCredential(conf, configuration)
	assert.NoError(t, err)
	assert.NotNil(t, credential)

	basicAuth := credential.(*basicAuthCredential)
	basicAuth.updateAuthPairFromDynamicConf()

	return credential
}

func Test_basicAuthCredential_Auth(t *testing.T) {
	t.Parallel()

	cred := makeCred(t)
	tests := []struct {
		name     string
		username string
		password string
		want     AuthInfo
		wantErr  bool
	}{
		{
			name:     "right password",
			username: "user-1",
			password: "123456",
			want: BasicAuthInfo{
				Username: "user-1",
				Password: "123456",
			},
			wantErr: false,
		},
		{
			name:     "wrong password",
			username: "user-1",
			password: "123456789",
			want:     nil,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hr := &http.Request{
				Header: make(http.Header),
			}
			hr.SetBasicAuth(tt.username, tt.password)
			got, err := cred.Auth(hr)
			if (err != nil) != tt.wantErr {
				t.Errorf("Auth() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Auth() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_basicAuthCredential_AuthToken(t *testing.T) {
	t.Parallel()

	cred := makeCred(t)
	tests := []struct {
		name     string
		username string
		password string
		want     AuthInfo
		wantErr  bool
	}{
		{
			name:     "right password",
			username: "user-1",
			password: "123456",
			want: BasicAuthInfo{
				Username: "user-1",
				Password: "123456",
			},
			wantErr: false,
		},
		{
			name:     "wrong password",
			username: "user-1",
			password: "123456789",
			want:     nil,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := cred.AuthToken(makeBasicToken(tt.username, tt.password))
			if (err != nil) != tt.wantErr {
				t.Errorf("Auth() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Auth() got = %v, want %v", got, tt.want)
			}
		})
	}
}
