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
	"net/http"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

const SubjectNameAnonymous = "anonymous"

type AnonymousAuthInfo struct{}

func (a AnonymousAuthInfo) AuthType() AuthType {
	return AuthTypeInsecure
}

func (a AnonymousAuthInfo) SubjectName() string {
	return SubjectNameAnonymous
}

func NewInsecureCredential(_ *generic.AuthConfiguration, _ *dynamic.DynamicAgentConfiguration) (Credential, error) {
	return &insecureCredential{}, nil
}

type insecureCredential struct{}

func (i *insecureCredential) Run(_ context.Context) {
}

func (i *insecureCredential) AuthType() AuthType {
	return AuthTypeInsecure
}

func (i *insecureCredential) Auth(_ *http.Request) (AuthInfo, error) {
	return AnonymousAuthInfo{}, nil
}

func (i *insecureCredential) AuthToken(_ string) (AuthInfo, error) {
	return AnonymousAuthInfo{}, nil
}
