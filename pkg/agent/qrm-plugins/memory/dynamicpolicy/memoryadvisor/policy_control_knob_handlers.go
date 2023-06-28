package memoryadvisor

import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	v1 "k8s.io/api/core/v1"
)

// handler may trigger operations for corresponding control knobs
// or set control knob value to memory plugin checkpoint and take effect asynchronously
type MemoryControlKnobHandler func(entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries) error

type MemoryControKnobName string

const (
	ControKnobKeyMemoryLimitInBytes MemoryControKnobName = "memory_limit_in_bytes"
	ControKnobKeyDropCache          MemoryControKnobName = "drop_cache"
	ControKnobKeyCPUSetMems         MemoryControKnobName = "cpuset_mems"
)

var memoryControlKnobHandlers sync.Map

func RegisterControlKnobHandler(name MemoryControKnobName, handler MemoryControlKnobHandler) {
	memoryControlKnobHandlers.Store(name, handler)
}

func GetRegisteredControlKnobHandlers() map[MemoryControKnobName]MemoryControlKnobHandler {
	res := make(map[MemoryControKnobName]MemoryControlKnobHandler)
	memoryControlKnobHandlers.Range(func(key, value interface{}) bool {
		res[key.(MemoryControKnobName)] = value.(MemoryControlKnobHandler)
		return true
	})
	return res
}

func ControlKnobHandlerWithChecker(handler MemoryControlKnobHandler) MemoryControlKnobHandler {
	return func(entryName, subEntryName string,
		calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries) error {

		if calculationInfo == nil {
			return fmt.Errorf("handler got nil calculationInfo")
		} else if calculationInfo.CalculationResult == nil {
			return fmt.Errorf("handler got nil calculationInfo.CalculationResult")
		} else if calculationInfo.CgroupPath == "" &&
			podResourceEntries[v1.ResourceMemory][entryName][subEntryName] == nil {
			return fmt.Errorf("calculationInfo indicates no target")
		}

		if podResourceEntries[v1.ResourceMemory][entryName][subEntryName] != nil &&
			podResourceEntries[v1.ResourceMemory][entryName][subEntryName].ExtraControlKnobInfo == nil {
			podResourceEntries[v1.ResourceMemory][entryName][subEntryName].ExtraControlKnobInfo = make(map[string]state.ControlKnobInfo)
		}

		return handler(entryName, subEntryName, calculationInfo, podResourceEntries)
	}
}
