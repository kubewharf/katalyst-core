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

package provisioner

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/plugin/provisioner/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func init() {
	policy.RegisterInitializer(types.MemoryProvisionPolicyCanonical, policy.NewPolicyCanonical)
}

const (
	MemoryProvisioner = "memory-provisioner"

	metricMemoryNUMAProvision = "memory_numa_provision"
	metricTagKeyNUMAID        = "numa_id"
)

type memoryProvisioner struct {
	mutex            sync.RWMutex
	metaReader       metacache.MetaReader
	metaServer       *metaserver.MetaServer
	emitter          metrics.MetricEmitter
	memoryProvisions machine.MemoryDetails
	conf             *config.Configuration
	extraConfig      interface{}
	// policy
	policy policy.ProvisionPolicy
}

func NewMemoryProvisioner(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) plugin.MemoryAdvisorPlugin {
	mp := &memoryProvisioner{
		conf:             conf,
		extraConfig:      extraConfig,
		metaReader:       metaReader,
		metaServer:       metaServer,
		emitter:          emitter,
		memoryProvisions: make(machine.MemoryDetails),
	}
	general.Infof("[memory-provisioner] new memory provisioner")
	if err := mp.initializeMemoryProvisioner(); err != nil {
		general.Errorf("[memory-provisioner] initialize memory provisioner failed: %v", err)
		return nil
	}

	return mp
}

// initializeMemoryProvisioner initialize memory provisioner
func (m *memoryProvisioner) initializeMemoryProvisioner() error {
	// get policy
	policyName := m.conf.MemoryProvisionPolicy
	initializers := policy.GetRegisteredInitializers()

	initFunc, ok := initializers[policyName]
	general.Infof("[memory-provisioner] policyName: %s", policyName)
	if !ok {
		return fmt.Errorf("policy %s not found", policyName)
	}
	m.policy = initFunc(m.conf, nil, m.metaReader, m.metaServer, m.emitter)

	return nil
}

func (m *memoryProvisioner) Reconcile(status *types.MemoryPressureStatus) (err error) {
	reservedForAllocate := m.conf.GetDynamicConfiguration().GetReservedResourceForAllocate(v1.ResourceList{
		v1.ResourceMemory: *resource.NewQuantity(int64(m.metaServer.MemoryCapacity), resource.BinarySI),
	})[v1.ResourceMemory]
	m.policy.SetEssentials(
		types.ResourceEssentials{
			EnableReclaim:       m.conf.GetDynamicConfiguration().EnableReclaim,
			ResourceUpperBound:  float64(m.metaServer.MemoryCapacity),
			ReservedForAllocate: reservedForAllocate.AsApproximateFloat64(),
		})
	err = m.policy.Update()
	if err != nil {
		return err
	}
	return nil
}

func (m *memoryProvisioner) GetAdvices() types.InternalMemoryCalculationResult {
	result := types.InternalMemoryCalculationResult{}
	provision := m.policy.GetProvision()
	// marshal memory provisioner to json string
	provisionJson, err := json.Marshal(provision)
	if err != nil {
		general.Errorf("marshal memory provisioner failed: %v", err)
		return result
	}

	for numaID, v := range provision {
		m.emitter.StoreInt64(metricMemoryNUMAProvision, int64(v), metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{metricTagKeyNUMAID: strconv.Itoa(numaID)})...)
	}

	entry := types.ExtraMemoryAdvices{
		Values: map[string]string{
			string(memoryadvisor.ControlKnobReclaimedMemorySize): string(provisionJson),
		},
	}
	result.ExtraEntries = append(result.ExtraEntries, entry)
	return result
}
