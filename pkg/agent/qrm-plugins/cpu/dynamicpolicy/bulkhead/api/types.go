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

package api

import (
	"context"

	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	bypassutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type HandlerContext struct {
	bypassutil.BypassCPUSetAdjustmentHandlerCtx
	View *bulkheadutils.CPUSetPartitionView
}

type PeriodicalHandlerContext struct {
	CoreConf    *config.Configuration
	ExtraConf   interface{}
	DynamicConf *dynamicconfig.Configuration
	Emitter     metrics.MetricEmitter
	MetaServer  *metaserver.MetaServer
}

type Plugin interface {
	Name() string
	Enable(conf *dynamicconfig.Configuration) bool
	CPUSetAdjustmentHandler(context.Context, HandlerContext) error
	PeriodicalHandler(context.Context, PeriodicalHandlerContext) error
}

type DisabledTransitionHandler interface {
	CPUSetAdjustmentDisabledHandler(context.Context, HandlerContext) error
}

type PluginFactory func(conf *config.Configuration) Plugin
