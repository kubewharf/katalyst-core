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

package dynamicpolicy

import (
	"context"
	"time"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	MBM_Controller = "mbm_controller"
)

type StoppableComponent interface {
	agent.Component
	Stop()
}

// core controller of MBM
// it only interacts with MetricsReader to get current reading of momory bandwidth usage (todo)
// also, it enforces the MBM via ExternalManager (todo)
type mbmController struct {
	emitter                metrics.MetricEmitter
	externalManager        external.ExternalManager
	mbmThresholdPercentage int
	mbmScanInterval        time.Duration
	mbmControllerCancel    context.CancelFunc
}

func (m *mbmController) Run(ctx context.Context) {
	general.Infof("mbm controller is enabled and in effect")
	ctx, m.mbmControllerCancel = context.WithCancel(ctx)
	go m.run(ctx)
}

func (m *mbmController) run(ctx context.Context) {
	// todo: to start numa mc mem bandwidth usage scanning by periodical handler,
	//       and save metric into metricStore
}

func (m *mbmController) Stop() {
	if m.mbmControllerCancel != nil {
		m.mbmControllerCancel()
		m.mbmControllerCancel = nil
	}
}

func NewMBMController(emitter metrics.MetricEmitter, externalManager external.ExternalManager, threshold int, scanInterval time.Duration) StoppableComponent {
	return &mbmController{
		emitter:                emitter.WithTags(MBM_Controller),
		externalManager:        externalManager,
		mbmThresholdPercentage: threshold,
		mbmScanInterval:        scanInterval,
	}
}

var _ StoppableComponent = &mbmController{}
