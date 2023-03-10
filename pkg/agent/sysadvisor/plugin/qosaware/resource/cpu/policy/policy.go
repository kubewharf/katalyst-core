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

package policy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/policy/canonical"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

// Policy generates raw cpu resource provision result based on different algorithms
type Policy interface {
	// Update triggers an epoch of algorithm update
	Update()
	// GetProvisionResult returns the latest algorithm result
	GetProvisionResult() interface{}
}

// NewPolicy returns a policy based on policy name
func NewPolicy(policyName types.CPUAdvisorPolicyName, metaCache *metacache.MetaCache) (Policy, error) {
	switch policyName {
	case types.CPUAdvisorPolicyCanonical:
		return canonical.NewCanonicalPolicy(metaCache), nil
	default:
		// Use canonical policy as default
		return canonical.NewCanonicalPolicy(metaCache), nil
	}
}
