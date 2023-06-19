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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// todo: deprecate this

type PolicyCanonical struct {
	*PolicyBase
}

func NewPolicyCanonical(regionName string, _ *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) HeadroomPolicy {
	p := &PolicyCanonical{
		PolicyBase: NewPolicyBase(regionName, metaReader, metaServer, emitter),
	}

	return p
}

func (p *PolicyCanonical) Update() error {
	return nil
}

func (p *PolicyCanonical) GetHeadroom() (float64, error) {
	return p.headroom, nil
}
