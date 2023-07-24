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

package periodicalhandler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/config"
	configagent "github.com/kubewharf/katalyst-core/pkg/config/agent"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserveragent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func makeTestGenericContext(t *testing.T) *agent.GenericContext {
	genericCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{})
	assert.NoError(t, err)

	return &agent.GenericContext{
		GenericContext: genericCtx,
		MetaServer:     makeMetaServer(),
		PluginManager:  nil,
	}
}

func makeMetaServer() *metaserver.MetaServer {
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)

	return &metaserver.MetaServer{
		MetaAgent: &metaserveragent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology:      cpuTopology,
				ExtraNetworkInfo: &machine.ExtraNetworkInfo{},
			},
		},
		ExternalManager: external.InitExternalManager(&pod.PodFetcherStub{}),
	}
}

func TestPeriodicalHandlerManager(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	var lock sync.RWMutex
	agentCtx := makeTestGenericContext(t)
	_, pmgr, _ := NewPeriodicalHandlerManager(agentCtx, &config.Configuration{AgentConfiguration: &configagent.AgentConfiguration{}}, nil, "test_periodical_handler_mgr")

	rctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		pmgr.Run(rctx)
		wg.Done()
	}()

	testNum := 5

	gName := "test_group"
	hName := "test_handler"
	_ = RegisterPeriodicalHandler(gName, hName, func(coreConf *config.Configuration,
		extraConf interface{},
		dynamicConf *dynamicconfig.DynamicAgentConfiguration,
		emitter metrics.MetricEmitter,
		metaServer *metaserver.MetaServer) {
		lock.Lock()
		testNum = 10
		lock.Unlock()
	},
		time.Second)

	ticker := time.After(2 * time.Second)

testLoop1:
	for {
		lock.RLock()
		as.Equal(testNum, 5)
		lock.RUnlock()
		select {
		case <-ticker:
			break testLoop1
		default:
		}
		time.Sleep(time.Second)
	}

	ReadyToStartHandlersByGroup(gName)

	ticker = time.After(10 * time.Second)

testLoop2:
	for {
		lock.RLock()
		if testNum == 10 {
			lock.RUnlock()
			break
		}
		lock.RUnlock()

		select {
		case <-ticker:
			break testLoop2
		default:
		}
		time.Sleep(time.Second)
	}

	lock.RLock()
	as.Equal(testNum, 10)
	lock.RUnlock()
	cancel()
}
