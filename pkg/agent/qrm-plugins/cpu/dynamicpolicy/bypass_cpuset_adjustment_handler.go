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
	"fmt"
	"sort"
	"strings"

	bypassutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func (p *DynamicPolicy) RegisterBypassCPUSetAdjustmentHandler(name string, handler bypassutil.BypassCPUSetAdjustmentHandler) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("bypass cpuset adjustment handler name is empty")
	}
	if handler == nil {
		return fmt.Errorf("bypass cpuset adjustment handler %q is nil", name)
	}
	if p.bypassCPUSetAdjustmentHandlers == nil {
		p.bypassCPUSetAdjustmentHandlers = map[string]bypassutil.BypassCPUSetAdjustmentHandler{}
	}
	if _, ok := p.bypassCPUSetAdjustmentHandlers[name]; ok {
		return fmt.Errorf("bypass cpuset adjustment handler %q already registered", name)
	}
	p.bypassCPUSetAdjustmentHandlers[name] = handler
	return nil
}

func (p *DynamicPolicy) runBypassCPUSetAdjustmentHandlers(ctx context.Context) error {
	if len(p.bypassCPUSetAdjustmentHandlers) == 0 {
		return nil
	}

	var topology *machine.CPUTopology
	if p.machineInfo != nil {
		topology = p.machineInfo.CPUTopology
	}
	var dynamicConf *dynamicconfig.Configuration
	if p.dynamicConfig != nil {
		dynamicConf = p.dynamicConfig.GetDynamicConfiguration()
	}
	handlerCtx := bypassutil.BypassCPUSetAdjustmentHandlerCtx{
		CoreConf:    p.conf,
		DynamicConf: dynamicConf,
		Emitter:     p.emitter,
		MetaServer:  p.metaServer,
		State:       p.state,
		Topology:    topology,
	}

	names := make([]string, 0, len(p.bypassCPUSetAdjustmentHandlers))
	for name := range p.bypassCPUSetAdjustmentHandlers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		handler := p.bypassCPUSetAdjustmentHandlers[name]
		if err := handler(ctx, handlerCtx); err != nil {
			return fmt.Errorf("run bypass cpuset adjustment handler %q: %w", name, err)
		}
	}
	return nil
}
