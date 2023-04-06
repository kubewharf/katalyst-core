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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global/adminqos"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/metaserver/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

var (
	cpuBrokerInitializers sync.Map
)

func init() {
	RegisterCPUBrokerInitializer("none", broker.NewNoneBroker)
}

// RegisterCPUBrokerInitializer is used to register user-defined cpu resource plugin init functions
func RegisterCPUBrokerInitializer(name string, initFunc broker.BrokerInitFunc) {
	cpuBrokerInitializers.Store(name, initFunc)
}

func getCPUBrokerInitializer(name string) (broker.BrokerInitFunc, bool) {
	initFunc, ok := cpuBrokerInitializers.Load(name)
	if !ok {
		return nil, false
	}
	return initFunc.(broker.BrokerInitFunc), true
}

type cpuHeadroomManagerImpl struct {
	dynamicconfig.ConfigurationRegister
	*GenericHeadroomManager
}

func NewCPUHeadroomManager(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, headroomAdvisor hmadvisor.ResourceAdvisor) (manager.HeadroomManager, error) {
	cpuBroker := conf.CPUBroker
	brokerInitializer, ok := getCPUBrokerInitializer(cpuBroker)
	if !ok {
		return nil, fmt.Errorf("invalid broker name %v for cpu headroom manager", cpuBroker)
	}

	gm := NewGenericHeadroomManager(
		v1.ResourceCPU,
		true,
		true,
		conf.HeadroomReporterSyncPeriod,
		brokerInitializer(emitter, metaServer, conf),
		headroomAdvisor,
		emitter,
		generateCPUWindowOptions(conf.HeadroomReporterConfiguration),
		generateReclaimCPUOptions(conf.ReclaimedResourceConfiguration),
	)

	cm := &cpuHeadroomManagerImpl{
		GenericHeadroomManager: gm,
	}

	metaServer.ConfigurationManager.Register(cm)

	return cm, nil
}

// ApplyConfig apply dynamic reclaimed cpu options
func (m *cpuHeadroomManagerImpl) ApplyConfig(conf *config.DynamicConfiguration) {
	options := generateReclaimCPUOptions(conf.ReclaimedResourceConfiguration)
	m.UpdateReclaimOptions(options)
}

func generateCPUWindowOptions(conf *reporter.HeadroomReporterConfiguration) GenericSlidingWindowOptions {
	return GenericSlidingWindowOptions{
		SlidingWindowTime: conf.HeadroomReporterSlidingWindowTime,
		MinStep:           conf.HeadroomReporterSlidingWindowMinStep[v1.ResourceCPU],
		MaxStep:           conf.HeadroomReporterSlidingWindowMaxStep[v1.ResourceCPU],
	}
}

func generateReclaimCPUOptions(conf *adminqos.ReclaimedResourceConfiguration) GenericReclaimOptions {
	return GenericReclaimOptions{
		EnableReclaim:                 conf.EnableReclaim,
		ReservedResourceForReport:     conf.ReservedResourceForReport[v1.ResourceCPU],
		MinReclaimedResourceForReport: conf.MinReclaimedResourceForReport[v1.ResourceCPU],
	}
}
