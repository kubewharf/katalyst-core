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

package headroompolicy

import (
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
)

// Policy generates memory resource provision result based on different algorithms
type Policy interface {
	// Update triggers an epoch of algorithm update
	Update() error
	// GetHeadroom returns the latest algorithm result
	GetHeadroom() (resource.Quantity, error)
	// SetMemory setup the limit and reserved memory
	SetMemory(limit, reserved int64)
}

// NewPolicy returns a policy based on policy name
func NewPolicy(policyName types.MemoryAdvisorPolicyName, metaCache *metacache.MetaCache, metaServer *metaserver.MetaServer) (Policy, error) {
	switch policyName {
	case types.MemoryAdvisorPolicyCanonical:
		return NewPolicyCanonical(metaCache, metaServer), nil
	default:
		// Use canonical policy as default
		return NewPolicyCanonical(metaCache, metaServer), nil
	}
}
