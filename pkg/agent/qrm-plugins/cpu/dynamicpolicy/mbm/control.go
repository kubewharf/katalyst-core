package mbm

import (
	"context"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const MemoryBandwidthManagement = "mbm"

type NUMAStater interface {
	GetMachineState() state.NUMANodeMap
}

type controller struct {
	metricEmitter metrics.MetricEmitter
	metricReader  types.MetricsReader
	numaStater    NUMAStater
}

func (c controller) Run(ctx context.Context) {
	//TODO implement me
	panic("implement me")
}

func NewController(metricEmitter metrics.MetricEmitter, metricReader types.MetricsReader, stater NUMAStater) agent.Component {
	return &controller{
		metricEmitter: metricEmitter.WithTags(MemoryBandwidthManagement),
		metricReader:  metricReader,
		numaStater:    stater,
	}
}
