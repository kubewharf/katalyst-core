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

package cpueviction

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func makeMetaServer(metricsFetcher types.MetricsFetcher, cpuTopology *machine.CPUTopology) *metaserver.MetaServer {
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{},
	}

	metaServer.MetricsFetcher = metricsFetcher
	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}

	return metaServer
}

func makeState(topo *machine.CPUTopology) (qrmstate.State, error) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-makeState")
	if err != nil {
		return nil, fmt.Errorf("make tmp dir for checkpoint failed with error: %v", err)
	}
	return qrmstate.NewCheckpointState(tmpDir, "test", "test", topo, false)
}

func TestNewCPUPressureEviction(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	conf := config.NewConfiguration()
	metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)
	stateImpl, err := makeState(cpuTopology)
	as.Nil(err)

	eviction, err := NewCPUPressureEviction(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	as.Nil(err)
	as.NotNil(eviction)

	go eviction.Run(context.Background())
	time.Sleep(1 * time.Second)
}
