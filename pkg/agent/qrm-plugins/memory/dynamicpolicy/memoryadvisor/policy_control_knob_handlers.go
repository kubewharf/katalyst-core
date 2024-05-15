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

package memoryadvisor

import (
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// MemoryControlKnobHandler may trigger operations for corresponding control knobs
// or set control knob value to memory plugin checkpoint and take effect asynchronously
type MemoryControlKnobHandler func(
	coreConf *config.Configuration,
	extraConf interface{},
	dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo,
	podResourceEntries state.PodResourceEntries) error

var memoryControlKnobHandlers sync.Map

func RegisterControlKnobHandler(name MemoryControlKnobName, handler MemoryControlKnobHandler) {
	memoryControlKnobHandlers.Store(name, handler)
}

func GetRegisteredControlKnobHandlers() map[MemoryControlKnobName]MemoryControlKnobHandler {
	res := make(map[MemoryControlKnobName]MemoryControlKnobHandler)
	memoryControlKnobHandlers.Range(func(key, value interface{}) bool {
		res[key.(MemoryControlKnobName)] = value.(MemoryControlKnobHandler)
		return true
	})
	return res
}

func ControlKnobHandlerWithChecker(handler MemoryControlKnobHandler) MemoryControlKnobHandler {
	return func(
		coreConf *config.Configuration,
		extraConf interface{},
		dynamicConf *dynamicconfig.DynamicAgentConfiguration,
		emitter metrics.MetricEmitter,
		metaServer *metaserver.MetaServer,
		entryName, subEntryName string,
		calculationInfo *advisorsvc.CalculationInfo,
		podResourceEntries state.PodResourceEntries,
	) error {
		if calculationInfo == nil {
			return fmt.Errorf("handler got nil calculationInfo")
		} else if calculationInfo.CalculationResult == nil {
			return fmt.Errorf("handler got nil calculationInfo.CalculationResult")
		} else if emitter == nil {
			return fmt.Errorf("handler got nil emitter")
		} else if metaServer == nil {
			return fmt.Errorf("handler got nil metaServer")
		}

		if podResourceEntries[v1.ResourceMemory][entryName][subEntryName] != nil &&
			podResourceEntries[v1.ResourceMemory][entryName][subEntryName].ExtraControlKnobInfo == nil {
			podResourceEntries[v1.ResourceMemory][entryName][subEntryName].ExtraControlKnobInfo = make(map[string]commonstate.ControlKnobInfo)
		}

		return handler(coreConf, extraConf, dynamicConf,
			emitter, metaServer,
			entryName, subEntryName,
			calculationInfo, podResourceEntries)
	}
}
