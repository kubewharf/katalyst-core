package mbm

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/external/mbm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const MemoryBandwidthManagement = "mbm"

type NUMAStater interface {
	GetMachineState() state.NUMANodeMap
}

type Controller struct {
	metricEmitter metrics.MetricEmitter
	metricReader  types.MetricsReader
	numaStater    NUMAStater
	mbAdjust      mbm.MBAdjuster

	packageMap         map[int][]int // package id --> numa nodes in the package
	interval           time.Duration
	bandwidthThreshold int
}

func (c Controller) Run(ctx context.Context) {
	general.Infof("mbm controller is starting")
	wait.Until(c.run, c.interval, ctx.Done())
}

func (c Controller) run() {
	// adjust mem bandwidth based on mbw metrics
}

func NewController(metricEmitter metrics.MetricEmitter, metricReader types.MetricsReader,
	stater NUMAStater, mbAdjuster mbm.MBAdjuster,
	interval time.Duration, bandwidthThreshold int, packageMap map[int][]int,
) *Controller {
	return &Controller{
		metricEmitter:      metricEmitter.WithTags(MemoryBandwidthManagement),
		metricReader:       metricReader,
		numaStater:         stater,
		mbAdjust:           mbAdjuster,
		packageMap:         packageMap,
		interval:           interval,
		bandwidthThreshold: bandwidthThreshold,
	}
}
