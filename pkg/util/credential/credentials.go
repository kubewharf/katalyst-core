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
	"context"
	"fmt"
	"net/http"

	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

type AuthType string

const (
	AuthTypeBasicAuth = "Basic"
	AuthTypeInsecure  = "Insecure"
)

// AuthInfo defines the common interface for the auth information the users are interested in.
// A struct that implements AuthInfo can hold other information the corresponding protocol can provide besides the
// interface defined.
type AuthInfo interface {
	// AuthType return the authentication protocol.
	AuthType() AuthType
	// SubjectName return the subject name it holds.
	SubjectName() string
}

// Credential defines common interface for all authentication protocol(e.g., BasicAuth, JWT Token).
type Credential interface {
	// AuthType return the authentication protocol.
	AuthType() AuthType
	// Auth takes a http request parameter and uses corresponding protocol to retrieve AuthInfo from the request.
	Auth(r *http.Request) (AuthInfo, error)
	// AuthToken takes a raw token string parameter and uses corresponding protocol to retrieve AuthInfo from it.
	AuthToken(token string) (AuthInfo, error)
	// Run starts the Credential component
	Run(ctx context.Context)
}

type NewCredentialFunc func(authConfig *generic.AuthConfiguration, clientSet *client.GenericClientSet) (Credential, error)

var credentialInitializer = make(map[AuthType]NewCredentialFunc)

func RegisterCredentialInitializer(authType AuthType, initializer NewCredentialFunc) {
	credentialInitializer[authType] = initializer
}

func GetCredentialInitializer() map[AuthType]NewCredentialFunc {
	return credentialInitializer
}

func init() {
	RegisterCredentialInitializer(AuthTypeBasicAuth, NewBasicAuthCredential)
	RegisterCredentialInitializer(AuthTypeInsecure, NewInsecureCredential)
}

func GetCredential(genericConf *generic.GenericConfiguration, clientSet *client.GenericClientSet) (Credential, error) {
	credentialInitializer, ok := GetCredentialInitializer()[AuthType(genericConf.AuthConfiguration.AuthType)]
	if ok {
		cred, err := credentialInitializer(genericConf.AuthConfiguration, clientSet)
		if err != nil {
			return nil, fmt.Errorf("initialize credential failed,type: %v, err: %v", genericConf.AuthType, err)
		}

		return cred, nil
	} else {
		return nil, fmt.Errorf("unsupported credential type: %v", genericConf.AuthConfiguration.AuthType)
	}
}

func DefaultCredential() Credential {
	return &insecureCredential{}
}
