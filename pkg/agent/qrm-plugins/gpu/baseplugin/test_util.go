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

package baseplugin

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserveragent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func makeMetaServer(numCPUs, numNUMAs, socketNum int) *metaserver.MetaServer {
	cpuTopology, _ := machine.GenerateDummyCPUTopology(numCPUs, socketNum, numNUMAs)

	return &metaserver.MetaServer{
		MetaAgent: &metaserveragent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: cpuTopology,
			},
		},
	}
}

func GenerateTestBasePlugin(
	stateDirectory string, provider machine.GPUTopologyProvider, numCPUs, numNUMAs, socketNum int,
) (*BasePlugin, error) {
	qrmConfig := qrm.NewQRMPluginsConfiguration()
	qosConfig := generic.NewQoSConfiguration()
	stateImpl, err := state.NewCheckpointState(qrmConfig, stateDirectory, GPUPluginStateFileName, "test_gpu_policy", provider, false, metrics.DummyMetrics{})
	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	if err != nil {
		return nil, fmt.Errorf("GenerateFakeGenericContext failed: %v", err)
	}
	metaServer := makeMetaServer(numCPUs, numNUMAs, socketNum)
	basePlugin := &BasePlugin{
		QrmConfig:             qrmConfig,
		QosConfig:             qosConfig,
		State:                 stateImpl,
		GpuTopologyProvider:   provider,
		Emitter:               metrics.DummyMetrics{},
		MetaServer:            metaServer,
		AgentCtx:              &agent.GenericContext{GenericContext: genericCtx, MetaServer: metaServer},
		AssociatedDevicesName: sets.NewString("nvidia.com/gpu"),
	}
	return basePlugin, nil
}
