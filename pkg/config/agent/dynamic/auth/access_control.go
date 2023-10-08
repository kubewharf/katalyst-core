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

package auth

import (
	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type AccessControlConfig struct {
	AccessControlPolicies []v1alpha1.AccessControlPolicy
}

func NewAccessControlConfig() *AccessControlConfig {
	return &AccessControlConfig{
		AccessControlPolicies: make([]v1alpha1.AccessControlPolicy, 0),
	}
}

func (a *AccessControlConfig) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if authConfig := conf.AuthConfiguration; authConfig != nil && authConfig.Spec.Config.AccessControlConfig != nil &&
		authConfig.Spec.Config.AccessControlConfig.AccessControlPolicies != nil {
		a.AccessControlPolicies = authConfig.Spec.Config.AccessControlConfig.AccessControlPolicies
	}
}
