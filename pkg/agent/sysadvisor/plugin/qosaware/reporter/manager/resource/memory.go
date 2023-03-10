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

package resource

import (
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter/manager"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter/manager/broker"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/metaserver/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

var (
	memoryBrokerInitializers sync.Map
)

func init() {
	RegisterMemoryBrokerInitializer("none", broker.NewNoneBroker)
}

// RegisterMemoryBrokerInitializer is used to register user-defined memory broker init functions
func RegisterMemoryBrokerInitializer(name string, initFunc broker.BrokerInitFunc) {
	memoryBrokerInitializers.Store(name, initFunc)
}

func getMemoryBrokerInitializer(name string) (broker.BrokerInitFunc, bool) {
	initFunc, ok := memoryBrokerInitializers.Load(name)
	if !ok {
		return nil, false
	}
	return initFunc.(broker.BrokerInitFunc), true
}

type memoryManagerImpl struct {
	dynamicconfig.ConfigurationRegister
	*GenericHeadroomManager
}

func NewMemoryHeadroomManager(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, headroomAdvisor hmadvisor.ResourceAdvisor) (manager.HeadroomManager, error) {
	memoryBroker, ok := getMemoryBrokerInitializer(conf.MemoryBroker)
	if !ok {
		return nil, fmt.Errorf("invalid broker name %v for memory reclaimed resource plugin", conf.MemoryBroker)
	}

	gm := NewGenericHeadroomManager(
		v1.ResourceMemory,
		false,
		false,
		conf.HeadroomReporterSyncPeriod,
		memoryBroker(emitter, metaServer, conf),
		headroomAdvisor,
		emitter,
		generateMemoryWindowOptions(conf),
		generateReclaimedMemoryOptions(conf),
	)

	cm := &memoryManagerImpl{
		GenericHeadroomManager: gm,
	}

	return cm, nil
}

// ApplyConfig apply dynamic reclaimed memory options
// todo: register it to dynamic configuration manager in metaServer after the kcc crd already defined
func (m *memoryManagerImpl) ApplyConfig(conf *config.Configuration) {
	options := generateReclaimedMemoryOptions(conf)
	m.UpdateReclaimOptions(options)
}

func generateMemoryWindowOptions(conf *config.Configuration) GenericSlidingWindowOptions {
	return GenericSlidingWindowOptions{
		SlidingWindowTime: conf.HeadroomReporterSlidingWindowTime,
		MinStep:           conf.HeadroomReporterSlidingWindowMinStep[v1.ResourceMemory],
		MaxStep:           conf.HeadroomReporterSlidingWindowMaxStep[v1.ResourceMemory],
	}
}

func generateReclaimedMemoryOptions(conf *config.Configuration) GenericReclaimOptions {
	return GenericReclaimOptions{
		EnableReclaim:                 conf.EnableReclaim,
		ReservedResourceForReport:     conf.ReservedResourceForReport[v1.ResourceMemory],
		MinReclaimedResourceForReport: conf.MinReclaimedResourceForReport[v1.ResourceMemory],
	}
}
