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
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
)

const (
	secretSyncInterval = 30 * time.Second
)

func NewDynamicConfAccessControl(_ *generic.AuthConfiguration, dynamicConfig *dynamic.DynamicAgentConfiguration) (AccessControl, error) {
	return &dynamicConfAccessControl{
		dynamicConfig:        dynamicConfig,
		subjectToPermissions: map[string][]PermissionType{},
	}, nil
}

type dynamicConfAccessControl struct {
	mutex sync.RWMutex

	dynamicConfig        *dynamic.DynamicAgentConfiguration
	subjectToPermissions AuthRule
}

func (s *dynamicConfAccessControl) Run(ctx context.Context) {
	go wait.Until(s.updateAuthInfoFromDynamicConf, secretSyncInterval, ctx.Done())
}

func (s *dynamicConfAccessControl) Verify(authInfo credential.AuthInfo, targetResource PermissionType) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return verify(authInfo, targetResource, s.subjectToPermissions)
}

func (s *dynamicConfAccessControl) updateAuthInfoFromDynamicConf() {
	dynamicConfiguration := s.dynamicConfig.GetDynamicConfiguration()
	newRule := make(AuthRule)
	for _, policy := range dynamicConfiguration.AccessControlPolicies {
		permissions := policy.PolicyRule.Resources
		rule := make([]PermissionType, 0, len(permissions))
		for i := range permissions {
			rule = append(rule, PermissionType(permissions[i]))
		}
		newRule[policy.Username] = rule
	}
	s.mutex.Lock()
	s.subjectToPermissions = newRule
	s.mutex.Unlock()
}
