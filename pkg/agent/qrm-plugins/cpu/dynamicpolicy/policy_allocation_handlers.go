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
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicpolicyutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	resourcehelper "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
	rputil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

var ErrDefaultShareResidualQuantityMismatch = errors.New("default share residual quantity exceeds advised quantity")

type DefaultShareResidualQuantityError struct {
	AdvisedQuantity int
	ResidualSize    int
	AvailableCPUs   machine.CPUSet
	FixedCPUs       machine.CPUSet
	ResidualCPUs    machine.CPUSet
}

func (e *DefaultShareResidualQuantityError) Error() string {
	return fmt.Sprintf(
		"default share quantity %d is smaller than residual cpuset size %d, available: %s, fixed: %s, residual: %s",
		e.AdvisedQuantity, e.ResidualSize, e.AvailableCPUs.String(), e.FixedCPUs.String(), e.ResidualCPUs.String())
}

func (e *DefaultShareResidualQuantityError) Unwrap() error {
	return ErrDefaultShareResidualQuantityMismatch
}

type requestStateCompensatedError struct {
	err error
}

func (e *requestStateCompensatedError) Error() string {
	return e.err.Error()
}

func (e *requestStateCompensatedError) Unwrap() error {
	return e.err
}

type requestStateOwnershipLostError struct {
	err error
}

func (e *requestStateOwnershipLostError) Error() string {
	return e.err.Error()
}

func (e *requestStateOwnershipLostError) Unwrap() error {
	return e.err
}

func (e *requestStateOwnershipLostError) OwnershipLost() bool {
	return true
}

type dnbAllocationFailureError struct {
	adjustment error
	rollback   error
	persist    error
	restore    error
}

const (
	dnbErrorTraversalMaxVisits = 4096
)

type dnbErrorTraversalNode struct {
	err error
}

type dnbErrorVisitKey struct {
	typ reflect.Type
	ptr uintptr
}

func newDNBAllocationFailureError(adjustment, rollback, persist, restore error) error {
	return &dnbAllocationFailureError{
		adjustment: adjustment,
		rollback:   rollback,
		persist:    persist,
		restore:    restore,
	}
}

func (e *dnbAllocationFailureError) Error() string {
	return fmt.Sprintf("apply DNB allocation and reclaim floor failed: %v; state rollback error: %v; "+
		"state persistence error: %v; machine restore error: %v",
		e.adjustment, e.rollback, e.persist, e.restore)
}

func (e *dnbAllocationFailureError) Is(target error) bool {
	return e.walkCauses(func(cause error) bool {
		if safelyEqualErrors(cause, target) {
			return true
		}
		if _, nested := cause.(*dnbAllocationFailureError); nested {
			return false
		}
		if matcher, ok := cause.(interface{ Is(error) bool }); ok {
			return safelyCallErrorPredicate(func() bool {
				return matcher.Is(target)
			})
		}
		return false
	})
}

func (e *dnbAllocationFailureError) As(target interface{}) bool {
	targetValue := reflect.ValueOf(target)
	if !targetValue.IsValid() || targetValue.Kind() != reflect.Ptr || targetValue.IsNil() {
		return false
	}
	targetType := targetValue.Elem().Type()

	return e.walkCauses(func(cause error) bool {
		causeValue := reflect.ValueOf(cause)
		if causeValue.IsValid() && causeValue.Type().AssignableTo(targetType) {
			return safelyCallErrorPredicate(func() bool {
				targetValue.Elem().Set(causeValue)
				return true
			})
		}
		if _, nested := cause.(*dnbAllocationFailureError); nested {
			return false
		}
		if matcher, ok := cause.(interface{ As(interface{}) bool }); ok {
			return safelyCallErrorPredicate(func() bool {
				return matcher.As(target)
			})
		}
		return false
	})
}

func (e *dnbAllocationFailureError) causes() [4]error {
	return [4]error{e.adjustment, e.rollback, e.persist, e.restore}
}

func (e *dnbAllocationFailureError) walkCauses(matches func(error) bool) bool {
	// Error methods are synchronous Go contracts: custom Is, As, and Unwrap
	// implementations must return to their caller. This traversal bounds work
	// between such calls with a global visit budget and detects identifiable
	// cycles; it deliberately does not start timeout goroutines that could leak
	// when a broken custom method never returns.
	causes := e.causes()
	stack := make([]dnbErrorTraversalNode, 0, len(causes))
	for i := len(causes) - 1; i >= 0; i-- {
		stack = append(stack, dnbErrorTraversalNode{err: causes[i]})
	}
	visited := make(map[dnbErrorVisitKey]struct{})
	visits := 0

	for len(stack) > 0 {
		if visits >= dnbErrorTraversalMaxVisits {
			return false
		}
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		visits++
		if isNilError(node.err) {
			continue
		}
		if key, ok := dnbErrorIdentity(node.err); ok {
			if _, found := visited[key]; found {
				continue
			}
			visited[key] = struct{}{}
		}
		if matches(node.err) {
			return true
		}

		if nested, ok := node.err.(*dnbAllocationFailureError); ok && nested != nil {
			nestedCauses := nested.causes()
			stack = appendDNBErrorTraversalChildren(
				stack, nestedCauses[:], dnbErrorTraversalMaxVisits-visits)
			continue
		}
		unwrapped := safelyUnwrapErrors(node.err)
		stack = appendDNBErrorTraversalChildren(
			stack, unwrapped, dnbErrorTraversalMaxVisits-visits)
	}
	return false
}

func appendDNBErrorTraversalChildren(
	stack []dnbErrorTraversalNode,
	children []error,
	remainingVisits int,
) []dnbErrorTraversalNode {
	available := remainingVisits - len(stack)
	if available <= 0 {
		return stack
	}
	if len(children) > available {
		children = children[:available]
	}
	for i := len(children) - 1; i >= 0; i-- {
		stack = append(stack, dnbErrorTraversalNode{err: children[i]})
	}
	return stack
}

func isNilError(err error) bool {
	if err == nil {
		return true
	}
	value := reflect.ValueOf(err)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func dnbErrorIdentity(err error) (key dnbErrorVisitKey, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	value := reflect.ValueOf(err)
	switch value.Kind() {
	case reflect.Chan, reflect.Map, reflect.Ptr, reflect.UnsafePointer:
		return dnbErrorVisitKey{typ: value.Type(), ptr: value.Pointer()}, true
	default:
		return dnbErrorVisitKey{}, false
	}
}

func safelyEqualErrors(left, right error) (equal bool) {
	defer func() {
		if recover() != nil {
			equal = false
		}
	}()
	return left == right
}

func safelyCallErrorPredicate(predicate func() bool) (matched bool) {
	defer func() {
		if recover() != nil {
			matched = false
		}
	}()
	return predicate()
}

func safelyUnwrapErrors(err error) (causes []error) {
	defer func() {
		if recover() != nil {
			causes = nil
		}
	}()

	switch unwrapper := err.(type) {
	case interface{ Unwrap() []error }:
		return unwrapper.Unwrap()
	case interface{ Unwrap() error }:
		return []error{unwrapper.Unwrap()}
	default:
		return nil
	}
}

func (p *DynamicPolicy) sharedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest,
	persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("sharedCoresAllocationHandler got nil req")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.sharedCoresWithNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
	default:
		return p.sharedCoresWithoutNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
	}
}

func (p *DynamicPolicy) sharedCoresWithoutNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("sharedCoresAllocationHandler got nil request")
	}

	_, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}
	// Read the live bypass switch once for this whole operation; a watcher flip
	// takes effect on the next operation, not midway through this one.
	live := p.shouldBypassCPUSetAdjustment()

	machineState := p.state.GetMachineState()
	podEntries := p.state.GetPodEntries()
	pooledCPUs := machineState.GetFilteredAvailableCPUSet(p.config.reservedCPUs,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicated),
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding))
	// cores that are not allocatable from user binding need to be deducted from the pool.
	notAllocatablePoolsCPUs := state.GetUnitedPoolsCPUs(podEntries, state.IsForbiddenPool, commonstate.IsSystemPool)
	pooledCPUs = pooledCPUs.Difference(notAllocatablePoolsCPUs)

	if pooledCPUs.IsEmpty() {
		general.Errorf("pod: %s/%s, container: %s get empty pooledCPUs", req.PodNamespace, req.PodName, req.ContainerName)
		return nil, fmt.Errorf("get empty pooledCPUs")
	}

	pooledCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, pooledCPUs)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GetTopologyAwareAssignmentsByCPUSet failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GetTopologyAwareAssignmentsByCPUSet failed with error: %v", err)
	}
	excludeRampUpReclaimFloor := func() error {
		// The entering shared_cores ramp-up is non-NUMA-binding, so it belongs
		// to the global reclaim domain. Resolve it explicitly so the floor is
		// scoped to the domains this admission actually activates.
		enteringDomains, err := p.rampUpDomainForAllocation(&state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(
				req, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
		})
		if err != nil {
			return fmt.Errorf("resolve entering shared_cores ramp-up domains failed: %w", err)
		}
		rampUpReclaimFloor, err := p.deriveRampUpReclaimFloor(machineState, podEntries, enteringDomains)
		if err != nil {
			return fmt.Errorf("derive reclaim floor for shared_cores ramp-up failed: %w", err)
		}
		pooledCPUs = pooledCPUs.Difference(rampUpReclaimFloor)
		if pooledCPUs.IsEmpty() {
			return fmt.Errorf("shared_cores ramp-up cpuset is empty after excluding reclaim floor %s",
				rampUpReclaimFloor.String())
		}
		pooledCPUsTopologyAwareAssignments, err = machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, pooledCPUs)
		if err != nil {
			return fmt.Errorf("calculate shared_cores ramp-up assignments after excluding reclaim floor failed: %w", err)
		}
		return nil
	}

	needSet := true
	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	originAllocationInfo := allocationInfo.Clone()
	err = updateAllocationInfoByReq(req, allocationInfo)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s updateAllocationInfoByReq failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("updateAllocationInfoByReq failed with error: %v", err)
	}
	// updateAllocationInfoByReq overwrites annotations with req.Annotations.
	// Restore the persisted cpuset-omission value so request annotations cannot
	// upgrade "false" to "true" or drop an existing key.
	inheritCPUSetOmission(allocationInfo, originAllocationInfo)
	if !cpusetOmissionAllowed(allocationInfo, live) {
		markCPUSetExposed(allocationInfo)
	}

	if allocationInfo == nil {
		general.Infof("pod: %s/%s, container: %s is met firstly, do ramp up with pooled cpus: %s",
			req.PodNamespace, req.PodName, req.ContainerName, pooledCPUs.String())

		shouldRampUp := p.shouldSharedCoresRampUp(ctx, req.PodUid)
		if shouldRampUp {
			if err := excludeRampUpReclaimFloor(); err != nil {
				return nil, err
			}
		}

		allocationInfo = &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
				commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
			RampUp:                           shouldRampUp,
			AllocationResult:                 pooledCPUs,
			OriginalAllocationResult:         pooledCPUs.Clone(),
			TopologyAwareAssignments:         pooledCPUsTopologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(pooledCPUsTopologyAwareAssignments),
			InitTimestamp:                    time.Now().Format(util.QRMTimeFormat),
			RequestQuantity:                  reqFloat64,
		}
		// Brand-new MAIN container: record the initial omission decision. It is
		// folded into this handler's own commit below.
		initNewMainContainerCPUSetOmission(allocationInfo, req.Annotations, live)

		if shouldRampUp {
			allocationInfo, err = p.doAndCheckPutAllocationInfo(ctx, allocationInfo, true, persistCheckpoint)
			if err != nil {
				return nil, err
			}
			needSet = false
		} else {
			targetPoolName := allocationInfo.GetSpecifiedPoolName()
			poolAllocationInfo := p.state.GetAllocationInfo(targetPoolName, commonstate.FakedContainerName)

			if poolAllocationInfo == nil {
				if p.isSharedCoresRampUpDisabled() {
					// cold-start bootstrap: the target pool entry is not ready yet, but
					// DisableSharedCoresRampUp=true forbids binding this pod to the broad
					// pooledCPUs. Delegate to the assembler pipeline (adjustPoolsAndIsolatedEntries
					// -> generatePoolsAndIsolation -> reviseReclaimPool) to seed the pool entry
					// together with this pod's allocation atomically, honoring overlap/pkg/numa
					// semantics without duplicating the logic here.
					general.Infof("pod: %s/%s, container: %s cold-start seeding target pool %s under DisableSharedCoresRampUp",
						req.PodNamespace, req.PodName, req.ContainerName, targetPoolName)

					if err := p.updateAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, originAllocationInfo, allocationInfo, persistCheckpoint); err != nil {
						return nil, err
					}
					checkedAllocationInfo, err := p.doAndCheckPutAllocationInfo(ctx, allocationInfo, true, persistCheckpoint)
					if err != nil {
						// roll back the freshly-inserted allocation to keep pod entries clean
						if rollbackErr := p.state.Delete(
							allocationInfo.PodUid, allocationInfo.ContainerName, persistCheckpoint); rollbackErr != nil {
							return nil, fmt.Errorf("cold-start seed pool %s failed: %v; rollback allocation: %w",
								targetPoolName, err, rollbackErr)
						}
						general.Errorf("pod: %s/%s, container: %s cold-start seed pool %s failed: %v",
							req.PodNamespace, req.PodName, req.ContainerName, targetPoolName, err)
						return nil, fmt.Errorf("cold-start seed pool %s failed: %v", targetPoolName, err)
					}

					_ = p.emitter.emitter.StoreInt64(util.MetricNameSharedCoresRampUpDisabledSeeded, 1,
						metrics.MetricTypeNameCount,
						metrics.MetricTag{Key: "poolName", Val: targetPoolName},
						metrics.MetricTag{Key: "overlap", Val: strconv.FormatBool(p.state.GetAllowSharedCoresOverlapReclaimedCores())},
					)

					allocationInfo = checkedAllocationInfo
					needSet = false
				} else {
					general.Infof("pod: %s/%s, container: %s is active, but its specified pool entry doesn't exist, try to ramp up it",
						req.PodNamespace, req.PodName, req.ContainerName)
					if err := excludeRampUpReclaimFloor(); err != nil {
						return nil, err
					}
					allocationInfo.RampUp = true
					allocationInfo.AllocationResult = pooledCPUs
					allocationInfo.OriginalAllocationResult = pooledCPUs.Clone()
					allocationInfo.TopologyAwareAssignments = pooledCPUsTopologyAwareAssignments
					allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(pooledCPUsTopologyAwareAssignments)
				}
			} else if p.isSharedCoresRampUpDisabled() {
				allocationInfo.AllocationResult = poolAllocationInfo.AllocationResult.Clone()
				allocationInfo.OriginalAllocationResult = poolAllocationInfo.OriginalAllocationResult.Clone()
				allocationInfo.TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolAllocationInfo.TopologyAwareAssignments)
				allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolAllocationInfo.OriginalTopologyAwareAssignments)
			} else {
				if err := p.updateAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, originAllocationInfo, allocationInfo, persistCheckpoint); err != nil {
					return nil, err
				}
				_, err = p.doAndCheckPutAllocationInfo(ctx, allocationInfo, false, persistCheckpoint)
				if err != nil {
					return nil, err
				}

				needSet = false
			}
		}
	} else if allocationInfo.RampUp {
		if util.PodInplaceUpdateResizing(req) {
			general.Errorf("pod: %s/%s, container: %s is still in ramp up, not allow to inplace update resize",
				req.PodNamespace, req.PodName, req.ContainerName)
			return nil, fmt.Errorf("pod is still ramp up, not allow to inplace update resize")
		}

		if err := excludeRampUpReclaimFloor(); err != nil {
			return nil, err
		}
		general.Infof("pod: %s/%s, container: %s is still in ramp up, allocate pooled cpus: %s",
			req.PodNamespace, req.PodName, req.ContainerName, pooledCPUs.String())

		allocationInfo.AllocationResult = pooledCPUs
		allocationInfo.OriginalAllocationResult = pooledCPUs.Clone()
		allocationInfo.TopologyAwareAssignments = pooledCPUsTopologyAwareAssignments
		allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(pooledCPUsTopologyAwareAssignments)
	} else {
		if util.PodInplaceUpdateResizing(req) {
			general.Infof("pod: %s/%s, container: %s request to inplace update resize (%.02f->%.02f)",
				req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.RequestQuantity, reqFloat64)
			allocationInfo.RequestQuantity = reqFloat64

			if !cpusetOmissionAllowed(allocationInfo, live) {
				markCPUSetExposed(allocationInfo)
			}
			if err := p.updateAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, originAllocationInfo, allocationInfo, persistCheckpoint); err != nil {
				return nil, err
			}
			_, err := p.doAndCheckPutAllocationInfoPodResizingAware(
				ctx, originAllocationInfo, allocationInfo, false, true, persistCheckpoint)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s doAndCheckPutAllocationInfoPodResizingAware failed: %q",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				if rollbackErr := p.state.SetAllocationInfo(
					originAllocationInfo.PodUid,
					originAllocationInfo.ContainerName,
					originAllocationInfo,
					persistCheckpoint,
				); rollbackErr != nil {
					return nil, fmt.Errorf("resize allocation failed: %v; restore original allocation: %w", err, rollbackErr)
				}
				return nil, err
			}
		} else {
			_, err := p.doAndCheckPutAllocationInfo(ctx, allocationInfo, true, persistCheckpoint)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s doAndCheckPutAllocationInfo failed: %q",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, err
			}
		}
		needSet = false
	}

	if needSet {
		// update pod entries directly.
		// if one of subsequent steps is failed,
		// we will delete current allocationInfo from podEntries in defer function of allocation function.
		if err := p.updateAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, originAllocationInfo, allocationInfo, persistCheckpoint); err != nil {
			return nil, err
		}
		podEntries := p.state.GetPodEntries()

		updatedMachineState, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		}
		if err := p.state.SetMachineState(updatedMachineState, persistCheckpoint); err != nil {
			return nil, fmt.Errorf("set shared allocation machine state: %w", err)
		}
	}

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req, allocationInfo.Annotations)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	if cpusetOmissionAllowed(allocationInfo, live) {
		clearCPUSetInAllocation(resp.AllocationResult)
	}
	return resp, nil
}

func (p *DynamicPolicy) reclaimedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("reclaimedCoresAllocationHandler got nil request")
	}

	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.allocationSidecarHandler(ctx, req, apiconsts.PodAnnotationQoSLevelReclaimedCores, persistCheckpoint)
	}

	_, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}
	// Read the live bypass switch once for this whole allocation operation so that
	// init / mark / response decisions agree even if the watcher flips mid-request;
	// a change takes effect on the next operation.
	live := p.shouldBypassCPUSetAdjustment()

	reclaimedAllocationInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	if reclaimedAllocationInfo == nil {
		general.Errorf("allocation for pod: %s/%s, container: %s is failed, because pool: %s is not ready",
			req.PodNamespace, req.PodName, req.ContainerName, commonstate.PoolNameReclaim)

		return nil, fmt.Errorf("pool: %s is not ready", commonstate.PoolNameReclaim)
	} else if reclaimedAllocationInfo.AllocationResult.Size() == 0 {
		general.Errorf("allocation for pod: %s/%s, container: %s is failed, because pool: %s is empty",
			req.PodNamespace, req.PodName, req.ContainerName, commonstate.PoolNameReclaim)

		return nil, fmt.Errorf("pool: %s is not empty", commonstate.PoolNameReclaim)
	}

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	originAllocationInfo := allocationInfo.Clone()
	if util.PodInplaceUpdateResizing(req) {
		if allocationInfo == nil {
			return nil, fmt.Errorf("pod request to cpu inplace update resize, but origin allocationInfo is nil")
		}
		if qosutil.AnnotationsIndicateNUMABinding(req.Annotations) != allocationInfo.CheckNUMABinding() {
			return nil, fmt.Errorf("can not change qos form non-rnb to rnb or vice versa during inplace update resize")
		}
		if allocationInfo.CheckNUMABinding() {
			reqNumaSet, err := machine.NewCPUSetUint64(req.Hint.Nodes...)
			if err != nil {
				return nil, fmt.Errorf("failed to parse request hint numa set: %v", err)
			}
			if !reqNumaSet.Equals(allocationInfo.GetAllocationResultNUMASet()) {
				return nil, fmt.Errorf("can not change the binding numa during inplace update resize")
			}
		}
		general.Infof("pod: %s/%s, container: %s request to cpu inplace update resize allocation, request: %.2f->%.2f",
			req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.RequestQuantity, reqFloat64)
		allocationInfo.RequestQuantity = reqFloat64
		// Mark the one-way exposure decision BEFORE the single persistent write:
		// SetAllocationInfo stores a clone, so marking after it would leave the
		// persisted state unmarked while the response carries the marked value.
		if !cpusetOmissionAllowed(allocationInfo, live) {
			markCPUSetExposed(allocationInfo)
		}
		if err := p.updateAllocationInfo(req.PodUid, req.ContainerName, originAllocationInfo, allocationInfo, persistCheckpoint); err != nil {
			return nil, err
		}
	} else {
		err = updateAllocationInfoByReq(req, allocationInfo)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s updateAllocationInfoByReq failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("updateAllocationInfoByReq failed with error: %v", err)
		}
		// Restore the persisted annotation value after updateAllocationInfoByReq
		// overwrote annotations with req.Annotations.
		inheritCPUSetOmission(allocationInfo, originAllocationInfo)
		if !cpusetOmissionAllowed(allocationInfo, live) {
			markCPUSetExposed(allocationInfo)
		}

		machineState := p.state.GetMachineState()
		// calculate NUMAs without actual numa_binding reclaimed pods
		nonReclaimActualBindingNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))
		if allocationInfo != nil {
			general.Infof("pod: %s/%s, container: %s with old allocation result: %s, allocate by reclaimedCPUSet: %s",
				req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.AllocationResult.String(), reclaimedAllocationInfo.AllocationResult.String())
		} else {
			general.Infof("pod: %s/%s, container: %s is firstly met, allocate by reclaimedCPUSet: %s",
				req.PodNamespace, req.PodName, req.ContainerName, reclaimedAllocationInfo.AllocationResult.String())

			allocationInfo = &state.AllocationInfo{
				AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
					commonstate.PoolNameReclaim, apiconsts.PodAnnotationQoSLevelReclaimedCores),
				InitTimestamp:   time.Now().Format(util.QRMTimeFormat),
				RequestQuantity: reqFloat64,
			}
			// Brand-new MAIN reclaimed container: record the initial omission
			// decision, folded into this handler's own commit below.
			initNewMainContainerCPUSetOmission(allocationInfo, req.Annotations, live)

			// calculate NUMAs without non-actual numa_binding reclaimed pods
			reclaimActualBindingNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedNonActualNUMABinding))
			// set reclaimed numa_binding NUMA ID to allocationInfo
			if req.Hint != nil && len(req.Hint.Nodes) == 1 && (reclaimActualBindingNUMAs.Contains(int(req.Hint.Nodes[0])) ||
				!nonReclaimActualBindingNUMAs.Equals(machine.NewCPUSet(int(req.Hint.Nodes[0])))) {
				allocationInfo.SetSpecifiedNUMABindingNUMAID(req.Hint.Nodes)
			}
		}

		// update reclaimed allocation result by pool entry
		err = p.updateReclaimAllocationResultByPoolEntry(allocationInfo, reclaimedAllocationInfo, nonReclaimActualBindingNUMAs)
		if err != nil {
			return nil, err
		}

		// update pod entries directly.
		// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
		if err := p.updateAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, originAllocationInfo, allocationInfo, persistCheckpoint); err != nil {
			return nil, err
		}

		// update reclaim non-actual numa_binding reclaim cores allocations if it needs to transfer a non-RNB numa to RNB numa
		podEntries := p.state.GetPodEntries()
		if allocationInfo.CheckActualNUMABinding() &&
			nonReclaimActualBindingNUMAs.Intersection(allocationInfo.AllocationResult).Size() > 0 {
			updatedNonReclaimActualBindingNUMAs := nonReclaimActualBindingNUMAs.Difference(allocationInfo.AllocationResult)
			err := p.updateNonActualNUMABindingReclaimCoresAllocations(podEntries, updatedNonReclaimActualBindingNUMAs, reclaimedAllocationInfo)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s updateNonActualNUMABindingReclaimCoresAllocations failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, err
			}
		}

		updatedMachineState, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, podEntries, machineState)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		}

		if err := p.state.SetMachineState(updatedMachineState, persistCheckpoint); err != nil {
			return nil, fmt.Errorf("set reclaimed allocation machine state: %w", err)
		}
	}

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req, allocationInfo.Annotations)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	if cpusetOmissionAllowed(allocationInfo, live) {
		clearCPUSetInAllocation(resp.AllocationResult)
	}

	return resp, nil
}

func shouldNumaBindingAllocationRampUp(
	podReclaimEnabled bool,
	eligibility *numaBindingPartitionEligibilitySnapshot,
) bool {
	return podReclaimEnabled || eligibility == nil
}

// updateReclaimAllocationResultByPoolEntry updates non-actual numa binding reclaimed allocation result by pool entry
func (p *DynamicPolicy) updateNonActualNUMABindingReclaimCoresAllocations(podEntries state.PodEntries,
	nonReclaimActualBindingNUMAs machine.CPUSet, poolEntry *state.AllocationInfo,
) error {
	nonActualNUMABindingAllocations := podEntries.GetFilteredPodEntries(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedNonActualNUMABinding))
	for _, containerEntries := range nonActualNUMABindingAllocations {
		for _, allocationInfo := range containerEntries {
			err := p.updateReclaimAllocationResultByPoolEntry(allocationInfo, poolEntry, nonReclaimActualBindingNUMAs)
			if err != nil {
				return fmt.Errorf("updateReclaimAllocationResultByPoolEntry with error: %v", err)
			}
		}
	}
	return nil
}

func (p *DynamicPolicy) dedicatedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresAllocationHandler got nil req")
	}

	if util.PodInplaceUpdateResizing(req) {
		return nil, fmt.Errorf("not support inplace update resize for dedicated cores")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.dedicatedCoresWithNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
	default:
		return p.dedicatedCoresWithoutNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
	}
}

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingAllocationHandler(_ context.Context,
	_ *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

// dedicatedCoresWithNUMABindingAllocationHandler plans the dedicated allocation
// and reclaim-floor update as one revision-CAS transaction. If physical apply
// fails after commit, rollback removes only this request's owned delta from the
// latest state, preserving unrelated concurrent allocations.
func (p *DynamicPolicy) dedicatedCoresWithNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.allocationSidecarHandler(ctx, req, apiconsts.PodAnnotationQoSLevelDedicatedCores, persistCheckpoint)
	}

	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
	sourcePodEntries, sourceMachineState, preCommitRevision := p.state.GetAdvisorStateSnapshot()
	sourceAllowOverlap := p.state.GetAllowSharedCoresOverlapReclaimedCores()
	sourceDisableDedicated := p.state.GetDisableDedicatedCoresOverlapReclaimedCores()
	if p.state.GetRevision() != preCommitRevision {
		return nil, fmt.Errorf("DNB admission source state changed while capturing overlap mode")
	}
	hardPartitionDisjoint := isRampUpReclaimHardPartitionEnabledWithConfig(dynamicConf) && sourceDisableDedicated
	basePodEntries := sourcePodEntries.Clone()
	baseMachineState := sourceMachineState.Clone()
	oldAllocationInfo := sourcePodEntries[req.PodUid][req.ContainerName]
	var allocationPreference *numaBindingAllocationPreference
	if oldAllocationInfo != nil {
		if shouldUseNumaBindingAllocationPreference(oldAllocationInfo) {
			allocationPreference = numaBindingAllocationPreferenceFromEntries(
				sourcePodEntries, req.PodUid, req.ContainerName)
		}
		reallocationMachinePodEntries := basePodEntries.Clone()
		removePodOwnerEntriesForReallocation(reallocationMachinePodEntries, req.PodUid, req.ContainerName)
		if basePodEntries[req.PodUid] != nil {
			delete(basePodEntries[req.PodUid], req.ContainerName)
			if len(basePodEntries[req.PodUid]) == 0 {
				delete(basePodEntries, req.PodUid)
			}
		}
		var err error
		baseMachineState, err = generateMachineStateFromPodEntries(
			p.machine.machineInfo.CPUTopology, reallocationMachinePodEntries, baseMachineState)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		}
	}
	machineState := baseMachineState

	podAggregatedRequest, _, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return nil, fmt.Errorf("GetPodAggregatedRequestResource failed with error: %v", err)
	}

	reqInt, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	podReclaimEnabled, err := p.podEnableReclaimForNumaBindingAllocation(ctx, req)
	if err != nil {
		return nil, err
	}

	result, hardReclaimCPUs, eligibility, err := p.allocateNumaBindingCPUsWithSnapshot(
		podAggregatedRequest, req.Hint, machineState, req.Annotations, podReclaimEnabled,
		allocationPreference, sourcePodEntries, sourceDisableDedicated, dynamicConf)
	if err != nil {
		general.ErrorS(err, "unable to allocate CPUs",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"podAggregatedRequest", podAggregatedRequest,
			"numCPUsInt", reqInt,
			"numCPUsFloat64", reqFloat64)
		return nil, err
	}
	if !hardReclaimCPUs.IsEmpty() {
		general.InfoS("ramp-up reclaim hard partition carved cpus out of dedicated allocation",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"hardReclaimCPUs", hardReclaimCPUs.String())
	}

	// avoid running services on not allocatable CPUs.
	notAllocatablePoolsCPUs := state.GetUnitedPoolsCPUs(
		sourcePodEntries, state.IsForbiddenPool, commonstate.IsSystemPool)
	result = result.Difference(notAllocatablePoolsCPUs)

	general.InfoS("allocate CPUs successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podAggregatedRequest", podAggregatedRequest,
		"numCPUsInt", reqInt,
		"numCPUsFloat64", reqFloat64,
		"result", result.String())

	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, result)
	if err != nil {
		general.ErrorS(err, "unable to calculate topologyAwareAssignments",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"podAggregatedRequest", podAggregatedRequest,
			"numCPUsInt", reqInt,
			"numCPUsFloat64", reqFloat64,
			"result cpuset", result.String())
		return nil, err
	}
	if qosutil.AnnotationsIndicateNUMAExclusive(req.Annotations) &&
		hardPartitionDisjoint {
		for _, numaID := range req.Hint.Nodes {
			numaState := machineState[int(numaID)]
			if numaState == nil {
				return nil, fmt.Errorf("NUMA-exclusive DNB ramp-up missing machine state for NUMA %d", numaID)
			}
			availableInNUMA := numaState.GetAvailableCPUSet(p.config.reservedCPUs)
			coverageTarget := availableInNUMA
			if eligibility != nil {
				coverageTarget = eligibility.partitionEligiblePerNUMA[int(numaID)]
			}
			floorInNUMA := hardReclaimCPUs.Intersection(coverageTarget)
			allocationInNUMA := result.Intersection(coverageTarget)
			if floorInNUMA.IsEmpty() {
				return nil, fmt.Errorf("NUMA-exclusive DNB ramp-up requires non-empty reclaim floor on NUMA %d", numaID)
			}
			if covered := allocationInNUMA.Union(floorInNUMA); !covered.Equals(coverageTarget) {
				return nil, fmt.Errorf("NUMA-exclusive DNB allocation and reclaim floor do not cover NUMA %d: allocation=%s floor=%s eligible=%s",
					numaID, allocationInNUMA.String(), floorInNUMA.String(), coverageTarget.String())
			}
		}
	}

	rampUp := shouldNumaBindingAllocationRampUp(podReclaimEnabled, eligibility)
	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.PoolNameDedicated, apiconsts.PodAnnotationQoSLevelDedicatedCores),
		RampUp:                           rampUp,
		AllocationResult:                 result.Clone(),
		OriginalAllocationResult:         result.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		InitTimestamp:                    time.Now().Format(util.QRMTimeFormat),
		RequestQuantity:                  reqFloat64,
	}
	// Brand-new MAIN dedicated container gets an initial omission decision only
	// when there is no existing entry; a re-admitted dedicated container inherits
	// its persisted value so a request annotation or live flip cannot alter it.
	// Dedicated is never bypassable, so the resolved value is always "false".
	live := p.shouldBypassCPUSetAdjustment()
	if oldAllocationInfo == nil {
		initNewMainContainerCPUSetOmission(allocationInfo, req.Annotations, live)
	} else {
		inheritCPUSetOmission(allocationInfo, oldAllocationInfo)
		if !cpusetOmissionAllowed(allocationInfo, live) {
			markCPUSetExposed(allocationInfo)
		}
	}

	numaNumber, err := qosutil.AnnotationsGetNUMANumber(req.Annotations, len(machineState), p.config.numaNumberAnnotationKey)
	if err != nil {
		return nil, fmt.Errorf("get numa number failed with error: %v", err)
	}
	// Cross NUMA allocation is only possible in the case of exclusive NUMA and non-exclusive NUMA with numa number more than 1
	if !qosutil.AnnotationsIndicateNUMAExclusive(req.Annotations) && numaNumber <= 1 {
		if len(req.Hint.Nodes) != 1 {
			return nil, fmt.Errorf("numa binding without numa exclusive allocation result numa node size is %d, "+
				"not equal to 1", len(req.Hint.Nodes))
		}
		allocationInfo.SetSpecifiedNUMABindingNUMAID(req.Hint.Nodes)
	}

	if basePodEntries[allocationInfo.PodUid] == nil {
		basePodEntries[allocationInfo.PodUid] = make(state.ContainerEntries)
	}
	admittedResult := machine.NewCPUSet()
	admittedOriginalResult := machine.NewCPUSet()
	var admittedAssignments, admittedOriginalAssignments map[int]machine.CPUSet
	if hardPartitionDisjoint {
		admittedResult = allocationInfo.AllocationResult.Clone()
		admittedOriginalResult = allocationInfo.OriginalAllocationResult.Clone()
		admittedAssignments = machine.DeepcopyCPUAssignment(allocationInfo.TopologyAwareAssignments)
		admittedOriginalAssignments = machine.DeepcopyCPUAssignment(allocationInfo.OriginalTopologyAwareAssignments)
	}
	basePodEntries[allocationInfo.PodUid][allocationInfo.ContainerName] = allocationInfo.Clone()

	updatedMachineState, err := generateMachineStateFromPodEntries(
		p.machine.machineInfo.CPUTopology, basePodEntries, baseMachineState)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	planningState := state.NewTransientState(p.machine.machineInfo.CPUTopology)
	if err := planningState.CommitAdvisorState(
		basePodEntries,
		updatedMachineState,
		sourceAllowOverlap,
		sourceDisableDedicated,
		false,
	); err != nil {
		return nil, fmt.Errorf("initialize DNB ramp-up target state failed: %w", err)
	}
	planningPolicy := p.newRampUpPlanningPolicy(planningState)
	planningRevision := planningState.GetRevision()
	err = planningPolicy.adjustAllocationEntriesWithRampUpFloorAtRevision(
		basePodEntries, updatedMachineState, false, hardReclaimCPUs, false, planningRevision)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s putContainersAndAdjustAllocationEntriesWithoutAllocation failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("adjustallocationentries failed with error: %v", err)
	}
	finalPodEntries := planningState.GetPodEntries()
	finalMachineState := planningState.GetMachineState()
	allocationInfo = finalPodEntries[req.PodUid][req.ContainerName]
	if allocationInfo == nil {
		return nil, fmt.Errorf("DNB allocation missing from planned state: pod=%s container=%s",
			req.PodUid, req.ContainerName)
	}
	if hardPartitionDisjoint {
		// Pool/reclaim planning may update ownership metadata, but the floor-first
		// allocator is the sole authority for this admission's physical CPU result.
		// Preserve that result rather than allowing generic ramp-up materialization to
		// expand it back across the protected reclaim floor.
		allocationInfo.AllocationResult = admittedResult
		allocationInfo.OriginalAllocationResult = admittedOriginalResult
		allocationInfo.TopologyAwareAssignments = admittedAssignments
		allocationInfo.OriginalTopologyAwareAssignments = admittedOriginalAssignments
		finalMachineState, err = generateMachineStateFromPodEntries(
			p.machine.machineInfo.CPUTopology, finalPodEntries, baseMachineState)
		if err != nil {
			return nil, fmt.Errorf("rebuild machine state after preserving DNB admission allocation failed: %w", err)
		}
	}
	reclaimInfo := finalPodEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	if hardPartitionDisjoint && (reclaimInfo == nil || !hardReclaimCPUs.IsSubsetOf(reclaimInfo.AllocationResult)) {
		reclaimCPUs := machine.NewCPUSet()
		if reclaimInfo != nil {
			reclaimCPUs = reclaimInfo.AllocationResult
		}
		return nil, fmt.Errorf("planned reclaim %s dropped DNB ramp-up floor %s",
			reclaimCPUs.String(), hardReclaimCPUs.String())
	}
	if hardPartitionDisjoint {
		if overlap := allocationInfo.AllocationResult.Intersection(hardReclaimCPUs); !overlap.IsEmpty() {
			return nil, fmt.Errorf("planned DNB allocation overlaps ramp-up reclaim floor: allocation=%s floor=%s overlap=%s",
				allocationInfo.AllocationResult.String(), hardReclaimCPUs.String(), overlap.String())
		}
	}
	prepared, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: preCommitRevision,
		entries:          finalPodEntries,
		baseMachineState: finalMachineState,
		allowOverlap:     sourceAllowOverlap,
		disableDedicated: sourceDisableDedicated,
		persist:          persistCheckpoint,
		source:           "DNB admission",
		validate: func(entries state.PodEntries, ms state.NUMANodeMap, overlap, dedicated bool) error {
			return p.validatePendingAdvisorPartitionViewWithRampUpExclusionWithDynamicConfig(entries, ms, overlap, dedicated, dynamicConf)
		},
		dynamicConfig: dynamicConf,
	})
	if err != nil {
		return nil, fmt.Errorf("prepare DNB allocation and reclaim floor atomically failed: %w", err)
	}
	allocationInfo = prepared.entries[req.PodUid][req.ContainerName]
	resp, err := p.packResp(allocationInfo, string(v1.ResourceCPU),
		util.OCIPropertyNameCPUSetCPUs, false, true, req, allocationInfo.Annotations)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	if err := p.accompanyReg().AllocateAccompanyResource(req, resp); err != nil {
		return nil, fmt.Errorf("accompany resource AugmentAllocationResult failed with error: %v", err)
	}
	if err := p.commitPreparedCPUPartition(prepared); err != nil {
		return nil, fmt.Errorf("commit DNB allocation and reclaim floor atomically failed: %w", err)
	}
	adjustCtx, cancel := context.WithTimeout(ctx, cpuSetAdjustmentHandlerTimeout(p.config.conf))
	adjustErr := p.runCPUSetAdjustmentHandlers(adjustCtx, dynamicpolicyutil.CPUSetAdjustmentModeAdmission)
	cancel()
	if adjustErr != nil {
		stateRolledBack, rollbackErr, persistErr := p.rollbackFailedDNBAllocation(
			req.PodUid, req.ContainerName, oldAllocationInfo, allocationInfo,
			preCommitRevision, sourcePodEntries, sourceMachineState,
			sourceAllowOverlap, sourceDisableDedicated, persistCheckpoint, dynamicConf)
		var restoreErr error
		if stateRolledBack {
			restoreCtx, restoreCancel := context.WithTimeout(ctx, cpuSetAdjustmentHandlerTimeout(p.config.conf))
			restoreErr = p.runCPUSetAdjustmentHandlers(restoreCtx, dynamicpolicyutil.CPUSetAdjustmentModeRetry)
			restoreCancel()
			if restoreErr != nil {
				p.scheduleCPUSetAdjustmentRetry(dynamicpolicyutil.RetryReasonRestoreFailed)
			}
		}
		if persistErr != nil {
			p.scheduleCPUSetAdjustmentPersistenceRetry()
		}
		var ownershipLost *requestStateOwnershipLostError
		if errors.As(rollbackErr, &ownershipLost) {
			p.markCPUSetAdjustmentPersistenceRequired()
			p.scheduleCPUSetAdjustmentRetry(dynamicpolicyutil.RetryReasonOwnershipLost)
			err := newDNBAllocationFailureError(adjustErr, rollbackErr, persistErr, restoreErr)
			return nil, &requestStateCompensatedError{err: err}
		}
		err := newDNBAllocationFailureError(adjustErr, rollbackErr, persistErr, restoreErr)
		if stateRolledBack || rollbackErr == nil {
			return nil, &requestStateCompensatedError{err: err}
		}
		return nil, err
	}
	if cpusetOmissionAllowed(allocationInfo, live) {
		clearCPUSetInAllocation(resp.AllocationResult)
	}

	return resp, nil
}

// rollbackFailedDNBAllocation removes only the failed request's committed delta
// from the latest state. It replans pools and the ramp-up reclaim floor from
// that latest view so unrelated allocations committed while adjustment ran are
// preserved and cannot be replaced by a stale whole-state snapshot.
func (p *DynamicPolicy) rollbackFailedDNBAllocation(
	podUID, containerName string,
	previous, failedCandidate *state.AllocationInfo,
	sourceRevision uint64,
	sourceEntries state.PodEntries,
	sourceMachineState state.NUMANodeMap,
	sourceAllowOverlap, sourceDisableDedicated bool,
	persistCheckpoint bool,
	dynamicConf *dynamicconfig.Configuration,
) (bool, error, error) {
	latestRevision := p.state.GetRevision()
	latestEntries := p.state.GetPodEntries()
	latestCandidate := latestEntries[podUID][containerName]
	if latestCandidate == nil {
		return false, &requestStateOwnershipLostError{err: fmt.Errorf(
			"allocation %s/%s ownership lost: failed candidate was deleted; skip stale candidate rollback",
			podUID, containerName)}, nil
	}
	if !reflect.DeepEqual(latestCandidate, failedCandidate) {
		return false, &requestStateOwnershipLostError{err: fmt.Errorf(
			"allocation %s/%s ownership lost: allocation advanced after failed candidate; skip stale candidate rollback",
			podUID, containerName)}, nil
	}
	if latestRevision == sourceRevision+1 {
		if err := p.validatePendingAdvisorPartitionView(
			sourceEntries, sourceMachineState, sourceAllowOverlap, sourceDisableDedicated); err != nil {
			return false, fmt.Errorf("validate exact DNB source rollback: %w", err), nil
		}
		if err := p.state.CommitAdvisorStateIfRevision(
			latestRevision,
			sourceEntries,
			sourceMachineState,
			sourceAllowOverlap,
			sourceDisableDedicated,
			false,
		); err != nil {
			return false, fmt.Errorf("restore exact DNB source state: %w", err), nil
		}
		p.emitFinalPoolSizeMetrics(sourceEntries)
		if persistCheckpoint {
			if err := p.state.StoreState(); err != nil {
				return true, nil, fmt.Errorf("persist restored DNB source state: %w", err)
			}
		}
		return true, nil, nil
	}

	if previous != nil {
		latestEntries[podUID][containerName] = previous.Clone()
	} else {
		delete(latestEntries[podUID], containerName)
		if len(latestEntries[podUID]) == 0 {
			delete(latestEntries, podUID)
		}
	}

	latestMachineState, err := generateMachineStateFromPodEntries(
		p.machine.machineInfo.CPUTopology, latestEntries, p.state.GetMachineState())
	if err != nil {
		return false, fmt.Errorf("recompute machine state without failed DNB allocation: %w", err), nil
	}
	planningState := state.NewTransientState(p.machine.machineInfo.CPUTopology)
	if err := planningState.CommitAdvisorState(
		latestEntries,
		latestMachineState,
		p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		false,
	); err != nil {
		return false, fmt.Errorf("initialize failed DNB rollback state: %w", err), nil
	}
	planningPolicy := p.newRampUpPlanningPolicy(planningState)
	planningRevision := planningState.GetRevision()
	if err := planningPolicy.adjustAllocationEntriesWithRampUpFloorForModeAtRevision(
		latestEntries, latestMachineState, false, machine.NewCPUSet(), false,
		planningRevision, defaultShareMaterializationRecovery); err != nil {
		return false, fmt.Errorf("recompute pools and reclaim floor without failed DNB allocation: %w", err), nil
	}
	_, _, err = p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: latestRevision,
		entries:          planningState.GetPodEntries(),
		baseMachineState: planningState.GetMachineState(),
		allowOverlap:     p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		disableDedicated: p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		persist:          false,
		source:           "DNB rollback",
		validate: func(entries state.PodEntries, ms state.NUMANodeMap, overlap, dedicated bool) error {
			return p.validatePendingAdvisorPartitionViewWithRampUpExclusionWithDynamicConfig(entries, ms, overlap, dedicated, dynamicConf)
		},
		dynamicConfig: dynamicConf,
	})
	if err != nil {
		return false, err, nil
	}
	if persistCheckpoint {
		if storeErr := p.state.StoreState(); storeErr != nil {
			return true, nil, fmt.Errorf("persist replanned DNB rollback state: %w", storeErr)
		}
	}
	return true, nil, nil
}

// newRampUpPlanningPolicy creates an isolated policy view for speculative
// pool and machine-state calculation. It intentionally carries no locks or
// runtime handlers, so planning cannot expose transient state or touch cgroups.
func (p *DynamicPolicy) newRampUpPlanningPolicy(planningState state.State) *DynamicPolicy {
	return &DynamicPolicy{
		state:   planningState,
		advisor: advisorComponent{advisorMonitor: p.advisor.advisorMonitor}, machine: machineComponent{machineInfo: p.machine.machineInfo}, meta: metaComponent{metaServer: p.meta.metaServer}, emitter: emitterComponent{emitter: metrics.DummyMetrics{}}, reclaim: reclaimComponent{reservedReclaimedCPUsSize: p.reclaim.reservedReclaimedCPUsSize, reservedReclaimedCPUSet: p.reclaim.reservedReclaimedCPUSet.Clone(), reservedReclaimedTopologyAwareAssignments: machine.DeepcopyCPUAssignment(
			p.reclaim.reservedReclaimedTopologyAwareAssignments)}, config: configComponent{enableReclaimNUMABinding: p.config.enableReclaimNUMABinding, enableSNBHighNumaPreference: p.config.enableSNBHighNumaPreference, enableCPUAdvisor: p.config.enableCPUAdvisor, qosConfig: p.config.qosConfig, dynamicConfig: p.config.dynamicConfig, conf: p.config.conf, numaBindingResultAnnotationKey: p.config.numaBindingResultAnnotationKey, numaNumberAnnotationKey: p.config.numaNumberAnnotationKey, numaIDsAnnotationKey: p.config.numaIDsAnnotationKey, topologyAllocationAnnotationKey: p.config.topologyAllocationAnnotationKey, transitionPeriod: p.config.transitionPeriod, reservedCPUs: p.config.reservedCPUs.Clone()},
	}
}

// allocationSidecarHandler currently we set cpuset of sidecar to the cpuset of its main container
func (p *DynamicPolicy) allocationSidecarHandler(_ context.Context,
	req *pluginapi.ResourceRequest, qosLevel string, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	_, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	podEntries := p.state.GetPodEntries()
	if podEntries[req.PodUid] == nil {
		general.Infof("there is no pod entry, pod: %s/%s, sidecar: %s, waiting next reconcile",
			req.PodNamespace, req.PodName, req.ContainerName)
		return &pluginapi.ResourceAllocationResponse{}, nil
	}

	mainContainerAllocationInfo := podEntries[req.PodUid].GetMainContainerEntry()

	// todo: consider sidecar without reconcile in vpa
	if mainContainerAllocationInfo == nil {
		general.Infof("main container is not found for pod: %s/%s, sidecar: %s, waiting next reconcile",
			req.PodNamespace, req.PodName, req.ContainerName)
		return &pluginapi.ResourceAllocationResponse{}, nil
	}

	// Read the live bypass switch once for this whole operation.
	live := p.shouldBypassCPUSetAdjustment()
	// Capture the sidecar's previously-persisted omission value BEFORE
	// applySidecarAllocationInfoFromMainContainer overwrites its annotations from main.
	existingSidecar := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)

	// the sidecar container also support inplace update resize, update the allocation and machine state here
	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.EmptyOwnerPoolName, qosLevel),
		InitTimestamp:   time.Now().Format(util.QRMTimeFormat),
		RequestQuantity: reqFloat64,
	}
	// GenerateGenericContainerAllocationMeta copies the request annotations, which may
	// carry the omission key from the pod spec. The persistent annotation must never be
	// born from a request: drop any request-seeded value, then seed only the
	// previously-persisted sidecar value (so an existing "false" stays sticky), and let
	// the apply core merge from main with the same one-way rule.
	delete(allocationInfo.Annotations, cpuconsts.CPUSetOmissionAllowedAnnotationKey)
	if existingSidecar != nil && existingSidecar.Annotations != nil {
		if val, ok := existingSidecar.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]; ok {
			if allocationInfo.Annotations == nil {
				allocationInfo.Annotations = make(map[string]string)
			}
			allocationInfo.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey] = val
		}
	}
	p.applySidecarAllocationInfoFromMainContainer(allocationInfo, mainContainerAllocationInfo)
	if !cpusetOmissionAllowed(allocationInfo, live) {
		markCPUSetExposed(allocationInfo)
	}

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	if err := p.updateAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, nil, allocationInfo, persistCheckpoint); err != nil {
		return nil, err
	}
	podEntries = p.state.GetPodEntries()

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}
	if err := p.state.SetMachineState(updatedMachineState, persistCheckpoint); err != nil {
		return nil, fmt.Errorf("set sidecar allocation machine state: %w", err)
	}

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req, allocationInfo.Annotations)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	if cpusetOmissionAllowed(allocationInfo, live) {
		clearCPUSetInAllocation(resp.AllocationResult)
	}
	return resp, nil
}

func (p *DynamicPolicy) sharedCoresWithNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.allocationSidecarHandler(ctx, req, apiconsts.PodAnnotationQoSLevelSharedCores, persistCheckpoint)
	}

	// Read the live bypass switch once for this whole operation.
	live := p.shouldBypassCPUSetAdjustment()

	// there is no need to delete old allocationInfo for the container if it exists,
	// allocateSharedNumaBindingCPUs will re-calculate pool size and avoid counting same entry twice
	allocationInfo, err := p.allocateSharedNumaBindingCPUs(ctx, req, req.Hint, persistCheckpoint, live)
	if err != nil || allocationInfo == nil {
		general.ErrorS(err, "unable to allocate CPUs",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		return nil, err
	}

	general.InfoS("allocate CPUs successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"result", allocationInfo.AllocationResult.String())

	// there is no need to call SetPodEntries and SetMachineState,
	// since they are already done in doAndCheckPutAllocationInfo of allocateSharedNumaBindingCPUs
	resp, err := cpuutil.PackAllocationResponse(allocationInfo,
		string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req, allocationInfo.Annotations)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	if err := p.accompanyReg().AllocateAccompanyResource(req, resp); err != nil {
		return nil, fmt.Errorf("accompany resource AugmentAllocationResult failed with error: %v", err)
	}
	if cpusetOmissionAllowed(allocationInfo, live) {
		clearCPUSetInAllocation(resp.AllocationResult)
	}

	return resp, nil
}

type numaBindingPartitionEligibilitySnapshot struct {
	partitionEligiblePerNUMA map[int]machine.CPUSet
}

// allocateNumaBindingCPUs allocates CPUs for NUMA binding containers.
// It considers NUMA affinity, exclusive requirements, and resource package pinning.
// Steps:
//  1. Calculate the initial available CPUSet based on the TopologyHint (NUMA nodes).
//  2. If a Resource Package is specified in annotations:
//     a. If the package has pinned CPUs, restrict the available CPUs to the intersection of NUMA CPUs and Pinned CPUs.
//     b. If the package is not pinned but others are, exclude other packages' pinned CPUs.
//  3. Allocate CPUs from the calculated available set using the topology calculator.
func (p *DynamicPolicy) allocateNumaBindingCPUs(numCPUs int, hint *pluginapi.TopologyHint,
	machineState state.NUMANodeMap, reqAnnotations map[string]string, podReclaimEnabled bool,
) (machine.CPUSet, machine.CPUSet, error) {
	result, hardReclaimCPUs, _, err := p.allocateNumaBindingCPUsWithEligibility(
		numCPUs, hint, machineState, reqAnnotations, podReclaimEnabled)
	return result, hardReclaimCPUs, err
}

func (p *DynamicPolicy) allocateNumaBindingCPUsWithEligibility(numCPUs int, hint *pluginapi.TopologyHint,
	machineState state.NUMANodeMap, reqAnnotations map[string]string, podReclaimEnabled bool,
) (machine.CPUSet, machine.CPUSet, *numaBindingPartitionEligibilitySnapshot, error) {
	return p.allocateNumaBindingCPUsWithEligibilityAndPreference(
		numCPUs, hint, machineState, reqAnnotations, podReclaimEnabled, nil)
}

func (p *DynamicPolicy) allocateNumaBindingCPUsWithEligibilityAndPreference(numCPUs int, hint *pluginapi.TopologyHint,
	machineState state.NUMANodeMap, reqAnnotations map[string]string, podReclaimEnabled bool,
	preference *numaBindingAllocationPreference,
) (machine.CPUSet, machine.CPUSet, *numaBindingPartitionEligibilitySnapshot, error) {
	return p.allocateNumaBindingCPUsWithSnapshot(
		numCPUs, hint, machineState, reqAnnotations, podReclaimEnabled, preference,
		p.state.GetPodEntries(), p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		p.config.dynamicConfig.GetDynamicConfiguration())
}

func (p *DynamicPolicy) allocateNumaBindingCPUsWithSnapshot(numCPUs int, hint *pluginapi.TopologyHint,
	machineState state.NUMANodeMap, reqAnnotations map[string]string, podReclaimEnabled bool,
	preference *numaBindingAllocationPreference, committedEntries state.PodEntries,
	disableDedicatedOverlap bool, dynamicConf *dynamicconfig.Configuration,
) (machine.CPUSet, machine.CPUSet, *numaBindingPartitionEligibilitySnapshot, error) {
	distributeEvenlyAcrossNuma := qosutil.AnnotationsIndicateDistributeEvenlyAcrossNuma(reqAnnotations)
	fullPCPUsPairing := qosutil.AnnotationsIndicateFullPCPUsPairing(reqAnnotations)
	numaExclusive := qosutil.AnnotationsIndicateNUMAExclusive(reqAnnotations)
	numaNumber, err := qosutil.AnnotationsGetNUMANumber(reqAnnotations, len(machineState), p.config.numaNumberAnnotationKey)
	if err != nil {
		return machine.NewCPUSet(), machine.NewCPUSet(), nil, fmt.Errorf("get numa number failed with error: %v", err)
	}

	if hint == nil {
		return machine.NewCPUSet(), machine.NewCPUSet(), nil, fmt.Errorf("hint is nil")
	} else if len(hint.Nodes) == 0 {
		return machine.NewCPUSet(), machine.NewCPUSet(), nil, fmt.Errorf("hint is empty")
	} else if !qosutil.AnnotationsIndicateNUMABinding(reqAnnotations) {
		return machine.NewCPUSet(), machine.NewCPUSet(), nil, fmt.Errorf("request is not NUMA binding, which is unexpected")
	} else if !numaExclusive && numaNumber <= 1 && len(hint.Nodes) > 1 {
		return machine.NewCPUSet(), machine.NewCPUSet(), nil, fmt.Errorf("NUMA not exclusive binding container has request larger than 1 NUMA")
	} else if numaExclusive && fullPCPUsPairing {
		return machine.NewCPUSet(), machine.NewCPUSet(), nil, fmt.Errorf("NUMA exclusive and full pcpus pairing not supported at the same time")
	} else if numaExclusive && distributeEvenlyAcrossNuma {
		return machine.NewCPUSet(), machine.NewCPUSet(), nil, fmt.Errorf("NUMA exclusive and distribute evenly across numa not supported at the same time")
	}

	result := machine.NewCPUSet()
	alignedAvailableCPUs := machine.CPUSet{}
	alignedAvailableCPUsPerNUMA := make(map[uint64]machine.CPUSet)
	hintNodes := hint.Nodes
	pkgName := rputil.GetResourcePackageName(reqAnnotations)
	hardPartitionEnabled := isRampUpReclaimHardPartitionEnabledWithConfig(dynamicConf)
	hardPartitionDisjoint := hardPartitionEnabled && disableDedicatedOverlap
	coverExclusivePartition := hardPartitionDisjoint && numaExclusive
	var eligibility *numaBindingPartitionEligibilitySnapshot
	dedicatedEligiblePerNUMA := make(map[int]machine.CPUSet)
	reclaimEligiblePerNUMA := make(map[int]machine.CPUSet)
	if coverExclusivePartition {
		eligibilityNUMAs := hintNodes
		if !podReclaimEnabled {
			eligibilityNUMAs = make([]uint64, 0, p.machine.machineInfo.NumNUMANodes)
			for _, numaID := range p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
				eligibilityNUMAs = append(eligibilityNUMAs, uint64(numaID))
			}
		}
		dedicatedEligiblePerNUMA, reclaimEligiblePerNUMA, err = p.numaBindingPartitionEligibility(machineState, pkgName, eligibilityNUMAs)
		if err != nil {
			return machine.NewCPUSet(), machine.NewCPUSet(), nil, err
		}
		eligibility = &numaBindingPartitionEligibilitySnapshot{
			partitionEligiblePerNUMA: make(map[int]machine.CPUSet, len(hintNodes)),
		}
		for _, numaID := range eligibilityNUMAs {
			eligibility.partitionEligiblePerNUMA[int(numaID)] = dedicatedEligiblePerNUMA[int(numaID)].Union(reclaimEligiblePerNUMA[int(numaID)])
		}
	}
	dedicatedEligible := machine.NewCPUSet()
	reclaimEligible := machine.NewCPUSet()

	for _, numaNode := range hintNodes {
		if machineState[int(numaNode)] == nil {
			return machine.NewCPUSet(), machine.NewCPUSet(), nil,
				fmt.Errorf("missing machine state for hinted NUMA %d", numaNode)
		}
		availableCPUs := machineState[int(numaNode)].GetAvailableCPUSet(p.config.reservedCPUs)
		if coverExclusivePartition {
			availableCPUs = dedicatedEligiblePerNUMA[int(numaNode)].Union(reclaimEligiblePerNUMA[int(numaNode)])
		} else {
			pinnedCPUSetsInNUMA := make(map[string]machine.CPUSet)
			for resourcePackage, rpState := range machineState[int(numaNode)].ResourcePackageStates {
				if rpState != nil && !rpState.PinnedCPUSet.IsEmpty() {
					pinnedCPUSetsInNUMA[resourcePackage] = rpState.PinnedCPUSet
				}
			}
			if pkgName != "" && !pinnedCPUSetsInNUMA[pkgName].IsEmpty() {
				availableCPUs = availableCPUs.Intersection(pinnedCPUSetsInNUMA[pkgName])
			} else {
				for _, pinnedCPUs := range pinnedCPUSetsInNUMA {
					availableCPUs = availableCPUs.Difference(pinnedCPUs)
				}
			}
		}
		alignedAvailableCPUsPerNUMA[numaNode] = availableCPUs
		alignedAvailableCPUs = alignedAvailableCPUs.Union(availableCPUs)
		if coverExclusivePartition {
			dedicatedEligible = dedicatedEligible.Union(availableCPUs)
			reclaimEligible = reclaimEligible.Union(reclaimEligiblePerNUMA[int(numaNode)])
		}
	}

	// A disjoint hard floor may make the physical allocation smaller than the
	// request, regardless of whether this pod itself is reclaimable. Only allow
	// that floor-induced short allocation when the uncarved eligible capacity
	// could satisfy the request; unrelated capacity shortages still fail closed.
	allowShortAllocation := hardPartitionDisjoint && numCPUs <= alignedAvailableCPUs.Size()

	// The node-level floor covers every reclaim NUMA and is shared by all
	// ramp-up QoS paths. Dedicated selection only subtracts it from the current
	// hint's available CPUs; CPUs on other NUMAs remain protected for shared
	// ramp-up and the bulkhead reclaim partition.
	// Bind one committed-state snapshot for this admission so the ramp-up floor
	// derivation, the live-pool anchor, and the reclaim-free preferred view all
	// read the same committed reclaim-pool generation. Reading reclaim from live
	// state after this point would risk mixing state revisions within one plan.
	var hardReclaimCPUs machine.CPUSet
	if coverExclusivePartition && !podReclaimEnabled {
		hardReclaimCPUs, err = p.deriveSteadyReclaimFloor(reclaimEligiblePerNUMA)
	} else {
		// The entering NUMA-binding request contributes its own reclaim domain
		// (the NUMA(s) it is placed on); the rest of the floor comes from the
		// ramp-up domains already recorded in the state entries.
		enteringDomains, dErr := enteringNUMABindingRampUpDomains(reqAnnotations, hintNodes, p.machine.machineInfo.CPUTopology)
		if dErr != nil {
			return machine.NewCPUSet(), machine.NewCPUSet(), nil,
				fmt.Errorf("resolve entering numa-binding ramp-up domains failed: %w", dErr)
		}
		hardReclaimCPUs, err = p.deriveRampUpReclaimFloorForModeWithDynamicConfig(
			machineState, committedEntries, enteringDomains, disableDedicatedOverlap, dynamicConf)
	}
	if err != nil {
		return machine.NewCPUSet(), machine.NewCPUSet(), nil, fmt.Errorf("derive node-level reclaim floor failed: %w", err)
	}
	hardReclaimCPUs, err = p.selectNumaBindingReclaimPartitionWithPreference(
		hardReclaimCPUs,
		dedicatedEligiblePerNUMA,
		reclaimEligiblePerNUMA,
		hintNodes,
		coverExclusivePartition,
		preference,
	)
	if err != nil {
		return machine.NewCPUSet(), machine.NewCPUSet(), nil,
			fmt.Errorf("select NUMA binding reclaim partition failed: %w", err)
	}
	if !hardReclaimCPUs.IsEmpty() {
		general.InfoS("ramp-up reclaim hard partition selected node-level reclaim floor",
			"hints", hintNodes,
			"hardReclaimCPUs", hardReclaimCPUs.String(),
			"podReclaimEnabled", podReclaimEnabled)
	}

	// In ramp-up NUMA-exclusive fresh admission, the hard reclaim floor on each
	// hinted NUMA is the live, validated reclaim pool currently resident there
	// (the pending/advisor reclaim pool), not the static identity-bearing
	// reservedReclaimedCPUSet and not a cgroup read cache. The derived floor can
	// land on a freshly-selected core when the live reclaim is not part of the
	// available set; shrinking the DNB result around that fresh anchor leaves the
	// committed live floor (e.g. a relocated per-NUMA floor) swallowed by the DNB,
	// which trips the shadow per-NUMA reclaim-floor exhaustion guard even though
	// the NUMA has spare capacity. Anchor the hinted-NUMA floor on the live reclaim
	// cores instead. It is scoped to ramp-up hard-partition exclusive fresh
	// admission (coverExclusivePartition) and to fresh admission (preference ==
	// nil): the idempotent re-admission path already derives the committed floor
	// from the preference. It is deliberately NOT conditioned on podReclaimEnabled:
	// a NUMA-exclusive DNB whose own pod is not reclaim-enabled still takes over a
	// hinted NUMA whose committed live reclaim floor must be preserved; the steady
	// derivation it otherwise uses anchors on the static reserved identity and
	// swallows the relocated live floor, tripping the shadow per-NUMA
	// reclaim-floor guard.
	if coverExclusivePartition && preference == nil {
		hardReclaimCPUs = p.anchorExclusiveReclaimFloorToLivePool(
			hardReclaimCPUs, hintNodes, reclaimEligiblePerNUMA, committedEntries)
	}

	// Hard-partition admission constructs the dedicated result from the floor-free
	// view. This prevents an invalid intermediate allocation from swallowing the
	// reclaim floor and removes the need for a post-planning shrink.
	allocationAvailableCPUs := alignedAvailableCPUs
	allocationAvailableCPUsPerNUMA := alignedAvailableCPUsPerNUMA
	if hardPartitionDisjoint {
		allocationAvailableCPUs = alignedAvailableCPUs.Difference(hardReclaimCPUs)
		allocationAvailableCPUsPerNUMA = make(map[uint64]machine.CPUSet, len(alignedAvailableCPUsPerNUMA))
		for numaNode, cpus := range alignedAvailableCPUsPerNUMA {
			allocationAvailableCPUsPerNUMA[numaNode] = cpus.Difference(hardReclaimCPUs)
		}
	}

	// Non-exclusive DNB prefers CPUs outside both the committed reclaim pool and
	// the selected floor. Under strict disjoint hard partitioning the floor is
	// already absent from allocationAvailableCPUs, so it can never be borrowed and
	// removed later.
	preferredAvailableCPUs := machine.NewCPUSet()
	var preferredAvailableCPUsPerNUMA map[uint64]machine.CPUSet
	if !numaExclusive {
		reclaimCPUs := committedReclaimPoolCPUs(committedEntries).Union(hardReclaimCPUs)
		if !reclaimCPUs.IsEmpty() {
			preferredAvailableCPUsPerNUMA = make(map[uint64]machine.CPUSet, len(allocationAvailableCPUsPerNUMA))
			for numaNode, cpus := range allocationAvailableCPUsPerNUMA {
				preferredAvailableCPUsPerNUMA[numaNode] = cpus.Difference(reclaimCPUs)
			}
			preferredAvailableCPUs = allocationAvailableCPUs.Difference(reclaimCPUs)
		}
	}

	var alignedCPUs machine.CPUSet

	if numaExclusive {
		alignedCPUs = allocationAvailableCPUs.Clone()
	} else {
		allocationSize := numCPUs
		if allowShortAllocation && allocationAvailableCPUs.Size() < allocationSize {
			allocationSize = allocationAvailableCPUs.Size()
			if distributeEvenlyAcrossNuma {
				allocationSize -= allocationSize % len(hintNodes)
			}
		}
		if allocationSize <= 0 {
			return machine.NewCPUSet(), machine.NewCPUSet(), nil,
				fmt.Errorf("elastic NUMA binding dedicated capacity is empty after reclaim floor")
		}

		var err error
		if distributeEvenlyAcrossNuma {
			alignedCPUs, err = p.allocateEvenlyAcrossNUMAs(
				allocationSize, hintNodes, allocationAvailableCPUsPerNUMA, preferredAvailableCPUsPerNUMA)
			if err != nil {
				return machine.NewCPUSet(), machine.NewCPUSet(), nil,
					fmt.Errorf("allocateEvenlyAcrossNUMA failed with error: %v", err)
			}
		} else {
			alignedCPUs, err = p.takeByTopologyPreferring(
				allocationAvailableCPUs, preferredAvailableCPUs, allocationSize)
			if err != nil {
				general.ErrorS(err, "take cpu for NUMA not exclusive binding container failed",
					"hints", hintNodes,
					"alignedAvailableCPUs", allocationAvailableCPUs.String())

				return machine.NewCPUSet(), machine.NewCPUSet(), nil,
					fmt.Errorf("take cpu for NUMA not exclusive binding container failed with err: %v", err)
			}
		}
	}

	general.InfoS("allocate by hints",
		"hints", hintNodes,
		"alignedAvailableCPUs", alignedAvailableCPUs.String(),
		"alignedAllocatedCPUs", alignedCPUs)

	// currently, result equals to alignedCPUs,
	// maybe extend cpus not aligned to meet requirement later
	result = result.Union(alignedCPUs)
	if coverExclusivePartition {
		reclaimInHint := hardReclaimCPUs.Intersection(reclaimEligible)
		partitionEligible := dedicatedEligible.Union(reclaimEligible)
		if result.IsEmpty() {
			return machine.NewCPUSet(), machine.NewCPUSet(), nil,
				fmt.Errorf("exclusive disjoint dedicated result is empty")
		}
		if !result.Union(reclaimInHint).Equals(partitionEligible) {
			return machine.NewCPUSet(), machine.NewCPUSet(), nil,
				fmt.Errorf("exclusive dedicated and reclaim do not cover eligible partition")
		}
	}
	if result.Size() < numCPUs && !allowShortAllocation {
		general.Errorf("result cpus: %s in hint NUMA nodes: %d with size: %d can't meet cpus request: %d",
			result.String(), hintNodes, result.Size(), numCPUs)

		return machine.NewCPUSet(), machine.NewCPUSet(), nil, fmt.Errorf("results can't meet cpus request")
	}

	return result, hardReclaimCPUs, eligibility, nil
}

// allocateEvenlyAcrossNUMAs distributes the cpu request evenly across NUMA nodes.
// preferredCPUsPerNUMA, when non-nil, holds each NUMA's preferred (reclaim-free) subset:
// every NUMA takes its per-NUMA share from its preferred set first and only borrows the
// remainder from the full available set when the preferred set is short. A nil/empty
// preferred set for a NUMA degrades to a plain topology-aware take on the full set.
func (p *DynamicPolicy) allocateEvenlyAcrossNUMAs(numCPUs int, hintNodes []uint64,
	availableCPUsPerNUMA, preferredCPUsPerNUMA map[uint64]machine.CPUSet,
) (machine.CPUSet, error) {
	// First check if it is possible to evenly distribute cpus across NUMA nodes
	if numCPUs%len(hintNodes) != 0 {
		return machine.NewCPUSet(), fmt.Errorf("unable to evenly distribute cpus across numa nodes, request: %d, numa nodes: %d",
			numCPUs, len(hintNodes))
	}
	allocated := machine.NewCPUSet()

	cpusReqPerNuma := numCPUs / len(hintNodes)
	for _, numaNode := range hintNodes {
		availableCPUs := availableCPUsPerNUMA[numaNode]

		// Allocate the CPUs in current numa, preferring this NUMA's reclaim-free subset.
		allocatedCPUsInNUMA, err := p.takeByTopologyPreferring(availableCPUs, preferredCPUsPerNUMA[numaNode], cpusReqPerNuma)
		if err != nil {
			return machine.NewCPUSet(), fmt.Errorf("take cpu for distribute_evenly_across_numa container failed with err: %v", err)
		}
		allocated = allocated.Union(allocatedCPUsInNUMA)
	}

	return allocated, nil
}

// takeByTopologyPreferring takes numCPUs from available in a topology-aware way while
// preferring cpus in the preferred subset. It takes from preferred first and borrows the
// remainder from the rest of available only when preferred cannot fully satisfy the
// request. When preferred is empty it degrades to calculator.TakeByTopology over available.
// preferred is expected to be a subset of available.
func (p *DynamicPolicy) takeByTopologyPreferring(
	available, preferred machine.CPUSet, numCPUs int,
) (machine.CPUSet, error) {
	// Keep the preference advisory: reclaim state can be stale or may contain CPUs
	// outside the current resource-package/NUMA-filtered available set.
	preferred = preferred.Intersection(available)
	if preferred.IsEmpty() {
		return calculator.TakeByTopology(p.machine.machineInfo, available, numCPUs, true)
	}

	// Take (at most) the requested count from the preferred set first.
	takenPreferred := preferred
	if preferred.Size() > numCPUs {
		var err error
		takenPreferred, err = calculator.TakeByTopology(p.machine.machineInfo, preferred, numCPUs, true)
		if err != nil {
			return machine.NewCPUSet(), fmt.Errorf("take preferred cpus failed with error: %v", err)
		}
	}

	remainingReq := numCPUs - takenPreferred.Size()
	if remainingReq <= 0 {
		return takenPreferred, nil
	}

	// Borrow the remainder from the non-preferred cpus of the available set.
	remainingAvailable := available.Difference(takenPreferred)
	takenRemaining, err := calculator.TakeByTopology(p.machine.machineInfo, remainingAvailable, remainingReq, true)
	if err != nil {
		return machine.NewCPUSet(), fmt.Errorf("take remaining cpus failed with error: %v", err)
	}

	return takenPreferred.Union(takenRemaining), nil
}

func (p *DynamicPolicy) allocateSharedNumaBindingCPUs(ctx context.Context, req *pluginapi.ResourceRequest,
	hint *pluginapi.TopologyHint, persistCheckpoint bool, live bool,
) (*state.AllocationInfo, error) {
	if req == nil {
		return nil, fmt.Errorf("nil req")
	} else if hint == nil {
		return nil, fmt.Errorf("hint is nil")
	} else if len(hint.Nodes) == 0 {
		return nil, fmt.Errorf("hint is empty")
	} else if len(hint.Nodes) > 1 {
		return nil, fmt.Errorf("shared_cores with numa_binding container has request larger than 1 NUMA")
	}

	reqInt, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}
	// live is read once by the caller and threaded through so this operation's
	// init / mark / response decisions share one value.

	general.InfoS("allocateSharedNumaBindingCPUs by hints",
		"hints", hint.Nodes,
		"numCPUsInt", reqInt,
		"numCPUsFloat64", reqFloat64)

	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			// it will be put to correct pool in doAndCheckPutAllocationInfo
			commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
		InitTimestamp:   time.Now().Format(util.QRMTimeFormat),
		RequestQuantity: reqFloat64,
	}
	allocationInfo.SetSpecifiedNUMABindingNUMAID(hint.Nodes)

	if util.PodInplaceUpdateResizing(req) {
		originAllocationInfo := p.state.GetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName)
		if originAllocationInfo == nil {
			general.Errorf("pod: %s/%s, container: %s request to cpu inplace update resize alloation, but no origin allocation info, reject it",
				req.PodNamespace, req.PodName, req.ContainerName)
			return nil, fmt.Errorf("no origion cpu allocation info for inplace update resize")
		}

		if !originAllocationInfo.CheckSharedNUMABinding() {
			general.Errorf("pod: %s/%s, container: %s request to cpu inplace update resize allocation, but origin allocation info is not shared numa binding, reject it",
				req.PodNamespace, req.PodName, req.ContainerName)
			return nil, fmt.Errorf("cannot change from non-snb to snb during inplace update")
		}

		general.Infof("pod: %s/%s, container: %s request to cpu inplace update resize allocation (%.02f->%.02f)",
			req.PodNamespace, req.PodName, req.ContainerName, originAllocationInfo.RequestQuantity, allocationInfo.RequestQuantity)
		if err := p.updateAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, originAllocationInfo, allocationInfo, persistCheckpoint); err != nil {
			return nil, err
		}
		// Inherit the persisted annotation value; request annotations must not
		// upgrade or drop the persisted value on resize.
		inheritCPUSetOmission(allocationInfo, originAllocationInfo)
		if !cpusetOmissionAllowed(allocationInfo, live) {
			markCPUSetExposed(allocationInfo)
		}
		checkedAllocationInfo, err := p.doAndCheckPutAllocationInfoPodResizingAware(
			ctx, originAllocationInfo, allocationInfo, false, true, persistCheckpoint)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s request to cpu inplace update resize allocation, but doAndCheckPutAllocationInfoPodResizingAware failed: %q",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			if rollbackErr := p.state.SetAllocationInfo(
				originAllocationInfo.PodUid,
				originAllocationInfo.ContainerName,
				originAllocationInfo,
				persistCheckpoint,
			); rollbackErr != nil {
				return nil, fmt.Errorf("resize allocation failed: %v; restore original allocation: %w", err, rollbackErr)
			}
			return nil, fmt.Errorf("doAndCheckPutAllocationInfo failed with error: %v", err)
		}
		return checkedAllocationInfo, nil
	} else {
		// Non-resize admission. Distinguish a truly-new MAIN container (origin
		// absent: record the initial omission decision) from an existing container
		// being re-admitted (origin present: inherit the persisted value so a
		// persisted "false" can never be upgraded back to "true" by request
		// annotations or a live-switch flip).
		originAllocationInfo := p.state.GetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName)
		if originAllocationInfo == nil {
			initNewMainContainerCPUSetOmission(allocationInfo, req.Annotations, live)
		} else {
			inheritCPUSetOmission(allocationInfo, originAllocationInfo)
			if !cpusetOmissionAllowed(allocationInfo, live) {
				markCPUSetExposed(allocationInfo)
			}
		}
		allocationInfo.RampUp = p.shouldSharedCoresRampUp(ctx, req.PodUid)
		if !p.isRampUpReclaimHardPartitionEnabled() {
			if err := p.updateAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, nil, allocationInfo, persistCheckpoint); err != nil {
				return nil, err
			}
		}
		checkedAllocationInfo, err := p.doAndCheckPutAllocationInfo(ctx, allocationInfo, true, persistCheckpoint)
		if err != nil {
			return nil, fmt.Errorf("doandcheckputallocationinfo failed with error: %s", strings.ToLower(err.Error()))
		}
		return checkedAllocationInfo, nil
	}
}

// putAllocationsAndAdjustAllocationEntriesAtRevision calculates and generates the latest checkpoint
// - unlike adjustAllocationEntries, it will also consider AllocationInfo
func (p *DynamicPolicy) putAllocationsAndAdjustAllocationEntriesAtRevision(
	ctx context.Context,
	entries state.PodEntries,
	allocationInfos []*state.AllocationInfo,
	incrByReq bool,
	persistCheckpoint bool,
	expectedRevision uint64,
) error {
	return p.putAllocationsAndAdjustAllocationEntriesResizeAwareAtRevision(
		ctx, nil, entries, allocationInfos, incrByReq, false, persistCheckpoint, expectedRevision)
}

// putAllocationsAndAdjustAllocationEntriesResizeAware adjusts the allocation entries based on the given allocation infos,
// considering resize requests and resource package information.
func (p *DynamicPolicy) putAllocationsAndAdjustAllocationEntriesResizeAware(
	ctx context.Context,
	originAllocationInfos,
	allocationInfos []*state.AllocationInfo,
	incrByReq,
	podInplaceUpdateResizing,
	persistCheckpoint bool,
) (err error) {
	return p.putAllocationsAndAdjustAllocationEntriesResizeAwareAtRevision(
		ctx, originAllocationInfos, nil, allocationInfos, incrByReq, podInplaceUpdateResizing,
		persistCheckpoint, p.state.GetRevision())
}

func (p *DynamicPolicy) putAllocationsAndAdjustAllocationEntriesResizeAwareAtRevision(
	ctx context.Context,
	originAllocationInfos []*state.AllocationInfo,
	entries state.PodEntries,
	allocationInfos []*state.AllocationInfo,
	incrByReq,
	podInplaceUpdateResizing,
	persistCheckpoint bool,
	expectedRevision uint64,
) (err error) {
	start := time.Now()
	podUID, podName, podNamespace, containerName := "", "", "", ""
	rampUp := false
	allocationSize := 0
	qosLevel := ""
	if len(allocationInfos) > 0 && allocationInfos[0] != nil {
		ai := allocationInfos[0]
		podUID, podName, podNamespace, containerName = ai.PodUid, ai.PodName, ai.PodNamespace, ai.ContainerName
		rampUp = ai.RampUp
		allocationSize = ai.AllocationResult.Size()
		qosLevel = ai.QoSLevel
	}
	defer func() {
		general.Infof("cpu allocation: put allocations and adjust entries finished duration=%s err=%v pod=%s/%s uid=%s container=%s qos=%s ramp_up=%t allocation_size=%d allocation_count=%d incr_by_req=%t resizing=%t persist_checkpoint=%t",
			time.Since(start), err, podNamespace, podName, podUID, containerName, qosLevel, rampUp, allocationSize,
			len(allocationInfos), incrByReq, podInplaceUpdateResizing, persistCheckpoint)
	}()

	if len(allocationInfos) == 0 {
		return nil
	}
	if podInplaceUpdateResizing {
		if len(originAllocationInfos) != 1 && len(allocationInfos) != 1 {
			general.Errorf("cannot adjust allocation entries for invalid allocation infos")
			return fmt.Errorf("invalid inplace update resize allocation infos length")
		}
	}

	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()

	if entries == nil {
		entries = p.state.GetPodEntries()
	} else {
		entries = entries.Clone()
	}

	for _, allocationInfo := range allocationInfos {
		if allocationInfo == nil {
			return fmt.Errorf("found nil allocationInfo in input parameter")
		}
		if !allocationInfo.CheckShared() {
			return fmt.Errorf("put container with invalid qos level: %s into pool", allocationInfo.QoSLevel)
		}
		if entries[allocationInfo.PodUid] == nil {
			entries[allocationInfo.PodUid] = make(state.ContainerEntries)
		}
		entries[allocationInfo.PodUid][allocationInfo.ContainerName] = allocationInfo.Clone()

		poolName := allocationInfo.GetSpecifiedPoolName()
		if poolName == commonstate.EmptyOwnerPoolName {
			return fmt.Errorf("allocationInfo points to empty poolName")
		}
	}

	machineState := p.state.GetMachineState()
	numaResourcePackagePinnedCPUSet := machineState.GetNUMAResourcePackagePinnedCPUSet()
	sharedNUMABindingCPUIncrRatio := getSharedNUMABindingCPUIncrRatioWithConfig(dynamicConf)

	var poolsQuantityMap map[string]map[int]int
	enableReclaim := false
	if dynamicConf != nil {
		enableReclaim = dynamicConf.EnableReclaim
	}
	if p.config.enableCPUAdvisor &&
		!cpuutil.AdvisorDegradation(p.advisor.advisorMonitor.GetHealthy(), enableReclaim) {
		// if sys advisor is enabled, we believe the pools' ratio that sys advisor indicates
		csetMap, err := entries.GetFilteredPoolsCPUSetMap(state.IsResidentPool, commonstate.IsSystemPool)
		if err != nil {
			return fmt.Errorf("GetFilteredPoolsCPUSetMap failed with error: %v", err)
		}

		poolsQuantityMap = machine.ParseCPUAssignmentQuantityMap(csetMap)
		if podInplaceUpdateResizing {
			// adjust pool resize
			originAllocationInfo := originAllocationInfos[0]
			allocationInfo := allocationInfos[0]

			poolName, targetNumaID, resizeReqFloat64, err := p.calcPoolResizeRequest(originAllocationInfo, allocationInfo, entries)
			if err != nil {
				return fmt.Errorf("calcPoolResizeRequest cannot calc pool resize request: %q", err)
			}
			if _, ok := poolsQuantityMap[poolName]; !ok {
				poolsQuantityMap[poolName] = make(map[int]int)
			}

			// update the pool size
			poolsQuantityMap[poolName][targetNumaID] += int(math.Ceil(resizeReqFloat64))
			// return err will abort the procedure,
			// so there is no need to revert modifications made in parameter poolsQuantityMap
			if len(poolsQuantityMap[poolName]) > 1 {
				return fmt.Errorf("pool %s cross NUMA: %+v", poolName, poolsQuantityMap[poolName])
			}
		} else if incrByReq {
			err := state.CountAllocationInfosToPoolsQuantityMap(
				numaResourcePackagePinnedCPUSet,
				allocationInfos,
				poolsQuantityMap,
				p.getContainerRequestedCores,
				sharedNUMABindingCPUIncrRatio,
			)
			if err != nil {
				return fmt.Errorf("CountAllocationInfosToPoolsQuantityMap failed with error: %v", err)
			}
		}
	} else {
		// else we do sum(containers req) for each pool to get pools ratio
		var err error
		poolsQuantityMap, err = state.GetSharedQuantityMapFromPodEntries(
			numaResourcePackagePinnedCPUSet,
			entries,
			allocationInfos,
			p.getContainerRequestedCores,
			sharedNUMABindingCPUIncrRatio,
		)
		if err != nil {
			return fmt.Errorf("GetSharedQuantityMapFromPodEntries failed with error: %v", err)
		}

		if incrByReq || podInplaceUpdateResizing {
			if podInplaceUpdateResizing {
				general.Infof("pod: %s/%s, container: %s request to re-calc pool size for cpu inplace update resize",
					allocationInfos[0].PodNamespace, allocationInfos[0].PodName, allocationInfos[0].ContainerName)
			}
			// if advisor is disabled, qrm can re-calc the pool size exactly. we don't need to adjust the pool size.
			cErr := state.CountAllocationInfosToPoolsQuantityMap(
				numaResourcePackagePinnedCPUSet,
				allocationInfos,
				poolsQuantityMap,
				p.getContainerRequestedCores,
				sharedNUMABindingCPUIncrRatio,
			)
			if cErr != nil {
				return fmt.Errorf("CountAllocationInfosToPoolsQuantityMap failed with error: %v", cErr)
			}
		}
	}

	isolatedQuantityMap := state.GetIsolatedQuantityMapFromPodEntries(entries, allocationInfos, p.getContainerRequestedCores)
	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloorForModeAtRevision(
		ctx, poolsQuantityMap, isolatedQuantityMap, entries, machineState, persistCheckpoint,
		machine.NewCPUSet(), true, expectedRevision, defaultShareMaterializationNormal, dynamicConf)
	if err != nil {
		return fmt.Errorf("adjustpoolsandisolatedentries failed with error: %s", strings.ToLower(err.Error()))
	}

	return nil
}

func (p *DynamicPolicy) calcPoolResizeRequest(originAllocation, allocation *state.AllocationInfo, podEntries state.PodEntries) (string, int, float64, error) {
	poolName := allocation.GetPoolName()
	targetNumaID := commonstate.FakedNUMAID

	originPodAggregatedRequest, ok := originAllocation.GetPodAggregatedRequest()
	if !ok {
		containerEntries, ok := podEntries[originAllocation.PodUid]
		if !ok {
			general.Warningf("pod %s/%s container entries not exist", originAllocation.PodNamespace, originAllocation.PodName)
			originPodAggregatedRequest = 0
		} else {
			podAggregatedRequestSum := float64(0)
			for containerName, containerEntry := range containerEntries {
				if containerName == originAllocation.ContainerName {
					podAggregatedRequestSum += originAllocation.RequestQuantity
				} else {
					podAggregatedRequestSum += containerEntry.RequestQuantity
				}
			}
			originPodAggregatedRequest = podAggregatedRequestSum
		}
	}

	podAggregatedRequest, ok := allocation.GetPodAggregatedRequest()
	if !ok {
		containerEntries, ok := podEntries[originAllocation.PodUid]
		if !ok {
			general.Warningf("pod %s/%s container entries not exist", originAllocation.PodNamespace, originAllocation.PodName)
			podAggregatedRequest = 0
		} else {
			podAggregatedRequestSum := float64(0)
			for _, containerEntry := range containerEntries {
				podAggregatedRequestSum += containerEntry.RequestQuantity
			}
			podAggregatedRequest = podAggregatedRequestSum
		}
	}

	poolResizeQuantity := podAggregatedRequest - originPodAggregatedRequest
	if poolResizeQuantity < 0 {
		// We don't need to adjust pool size in inplace update scale in mode, wait advisor to adjust the pool size later.
		general.Infof("pod: %s/%s, container: %s request cpu inplace update scale in (%.02f->%.02f)",
			allocation.PodNamespace, allocation.PodName, allocation.ContainerName, originPodAggregatedRequest, podAggregatedRequest)
		poolResizeQuantity = 0
	} else {
		// We should adjust pool size in inplace update scale out mode with resizeReqFloat64, and then wait advisor to adjust the pool size later.
		general.Infof("pod: %s/%s, container: %s request cpu inplace update scale out (%.02f->%.02f)",
			allocation.PodNamespace, allocation.PodName, allocation.ContainerName, originPodAggregatedRequest, podAggregatedRequest)
	}

	// only support share cores inplace update resize now (include non-binding share cores and share cores with NUMA binding)
	if allocation.CheckSharedNUMABinding() {
		// check snb numa migrate for inplace update resize
		originTargetNumaID, err := state.GetSharedNUMABindingTargetNuma(originAllocation)
		if err != nil {
			return "", 0, 0, fmt.Errorf("failed to get origin target NUMA")
		}
		targetNumaID, err = state.GetSharedNUMABindingTargetNuma(allocation)
		if err != nil {
			return "", 0, 0, fmt.Errorf("failed to get target NUMA")
		}

		// the pod is migrated to a new NUMA if the NUMA changed.
		// the new pool should scale out the whole request size.
		// the old pool would be adjusted by advisor later.
		if originTargetNumaID != targetNumaID {
			poolResizeQuantity = podAggregatedRequest
			general.Infof("pod %s/%s request inplace update resize and it was migrate to a new NUMA (%d->%d), AggregatedPodRequest(%.02f)",
				allocation.PodNamespace, allocation.PodName, originTargetNumaID, targetNumaID, podAggregatedRequest)
		}

		// get snb pool name
		poolName, err = allocation.GetSpecifiedNUMABindingPoolName()
		if err != nil {
			return "", 0, 0, fmt.Errorf("GetSpecifiedNUMABindingPoolName for %s/%s/%s failed with error: %v",
				allocation.PodNamespace, allocation.PodName, allocation.ContainerName, err)
		}
	}

	if poolName == commonstate.EmptyOwnerPoolName {
		return "", 0, 0, fmt.Errorf("get poolName failed for %s/%s/%s",
			allocation.PodNamespace, allocation.PodName, allocation.ContainerName)
	}

	return poolName, targetNumaID, poolResizeQuantity, nil
}

// adjustAllocationEntries calculates and generates the latest checkpoint
// It fetches resource package items and updates the allocation entries accordingly.
// adjustAllocationOptions bundles the optional knobs for a single cpuset
// adjustment attempt. Required inputs (entries, machineState, persistCheckpoint)
// stay positional; the former telescoping WithXxx/AtRevision/WithContext wrappers
// build this struct so new call sites do not need yet another forwarding variant.
type adjustAllocationOptions struct {
	expectedRevision    uint64
	explicitRampUpFloor machine.CPUSet
	runCPUSetHandlers   bool
	defaultShareMode    defaultShareMaterializationMode
}

func (p *DynamicPolicy) adjustAllocationEntries(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
) error {
	return p.adjustAllocationEntriesWithOptions(
		context.Background(), entries, machineState, persistCheckpoint,
		adjustAllocationOptions{
			expectedRevision:    p.state.GetRevision(),
			explicitRampUpFloor: machine.NewCPUSet(),
			runCPUSetHandlers:   true,
			defaultShareMode:    defaultShareMaterializationNormal,
		})
}

func (p *DynamicPolicy) adjustAllocationEntriesAtRevision(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	expectedRevision uint64,
) error {
	return p.adjustAllocationEntriesWithOptions(
		context.Background(), entries, machineState, persistCheckpoint,
		adjustAllocationOptions{
			expectedRevision:    expectedRevision,
			explicitRampUpFloor: machine.NewCPUSet(),
			runCPUSetHandlers:   true,
			defaultShareMode:    defaultShareMaterializationNormal,
		})
}

func (p *DynamicPolicy) adjustAllocationEntriesAtRevisionWithContext(
	ctx context.Context,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	expectedRevision uint64,
) error {
	if p.adjustAtRevisionWithContextHook != nil {
		return p.adjustAtRevisionWithContextHook(ctx, entries, machineState, persistCheckpoint, expectedRevision)
	}
	return p.adjustAllocationEntriesWithOptions(
		ctx, entries, machineState, persistCheckpoint,
		adjustAllocationOptions{
			expectedRevision:    expectedRevision,
			explicitRampUpFloor: machine.NewCPUSet(),
			runCPUSetHandlers:   true,
			defaultShareMode:    defaultShareMaterializationNormal,
		})
}

func (p *DynamicPolicy) adjustAllocationEntriesForRecoveryAtRevision(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	expectedRevision uint64,
) error {
	return p.adjustAllocationEntriesForRecoveryAtRevisionWithContext(
		context.Background(), entries, machineState, persistCheckpoint, expectedRevision)
}

func (p *DynamicPolicy) adjustAllocationEntriesForRecoveryAtRevisionWithContext(
	ctx context.Context,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	expectedRevision uint64,
) error {
	if p.adjustForRecoveryAtRevisionWithContextHook != nil {
		return p.adjustForRecoveryAtRevisionWithContextHook(ctx, entries, machineState, persistCheckpoint, expectedRevision)
	}
	if err := p.adjustAllocationEntriesWithOptions(
		ctx, entries, machineState, persistCheckpoint,
		adjustAllocationOptions{
			expectedRevision:    expectedRevision,
			explicitRampUpFloor: machine.NewCPUSet(),
			runCPUSetHandlers:   false,
			defaultShareMode:    defaultShareMaterializationRecovery,
		}); err != nil {
		return err
	}
	p.markCPUSetAdjustmentDirty(dynamicpolicyutil.RetryReasonRecoveryCommit)
	return nil
}

func (p *DynamicPolicy) adjustAllocationEntriesAfterDeletionAtRevision(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	expectedRevision uint64,
) error {
	if p.adjustAfterDeletionAtRevisionHook != nil {
		return p.adjustAfterDeletionAtRevisionHook(entries, machineState, persistCheckpoint, expectedRevision)
	}
	return p.adjustAllocationEntriesAfterDeletionAtRevisionWithContext(
		context.Background(), entries, machineState, persistCheckpoint, expectedRevision)
}

func (p *DynamicPolicy) adjustAllocationEntriesAfterDeletionAtRevisionWithContext(
	ctx context.Context,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	expectedRevision uint64,
) error {
	if p.adjustAfterDeletionAtRevisionWithContextHook != nil {
		return p.adjustAfterDeletionAtRevisionWithContextHook(ctx, entries, machineState, persistCheckpoint, expectedRevision)
	}
	// Preserve the normal adjustment path when current advisor quantities can
	// represent the deletion. If only the default-share quantity gate is stale,
	// retry the same CAS in recovery mode and leave cgroup convergence dirty.
	p.cleanPoolsFromPodEntries(entries)
	err := p.adjustAllocationEntriesAtRevisionWithContext(
		ctx, entries, machineState, persistCheckpoint, expectedRevision)
	if err == nil || !isDefaultShareResidualQuantityGateError(err) {
		return err
	}
	general.Warningf("retry pod deletion in default-share recovery mode: %v", err)
	return p.adjustAllocationEntriesForRecoveryAtRevisionWithContext(
		ctx, entries, machineState, persistCheckpoint, expectedRevision)
}

func (p *DynamicPolicy) adjustAllocationEntriesWithRampUpFloor(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	explicitRampUpFloor machine.CPUSet,
	runCPUSetHandlers bool,
) error {
	return p.adjustAllocationEntriesWithOptions(
		context.Background(), entries, machineState, persistCheckpoint,
		adjustAllocationOptions{
			expectedRevision:    p.state.GetRevision(),
			explicitRampUpFloor: explicitRampUpFloor,
			runCPUSetHandlers:   runCPUSetHandlers,
			defaultShareMode:    defaultShareMaterializationNormal,
		})
}

func (p *DynamicPolicy) adjustAllocationEntriesWithRampUpFloorAtRevision(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	explicitRampUpFloor machine.CPUSet,
	runCPUSetHandlers bool,
	expectedRevision uint64,
) error {
	return p.adjustAllocationEntriesWithOptions(
		context.Background(), entries, machineState, persistCheckpoint,
		adjustAllocationOptions{
			expectedRevision:    expectedRevision,
			explicitRampUpFloor: explicitRampUpFloor,
			runCPUSetHandlers:   runCPUSetHandlers,
			defaultShareMode:    defaultShareMaterializationNormal,
		})
}

func (p *DynamicPolicy) adjustAllocationEntriesWithRampUpFloorForModeAtRevision(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	explicitRampUpFloor machine.CPUSet,
	runCPUSetHandlers bool,
	expectedRevision uint64,
	defaultShareMode defaultShareMaterializationMode,
) error {
	return p.adjustAllocationEntriesWithOptions(
		context.Background(), entries, machineState, persistCheckpoint,
		adjustAllocationOptions{
			expectedRevision:    expectedRevision,
			explicitRampUpFloor: explicitRampUpFloor,
			runCPUSetHandlers:   runCPUSetHandlers,
			defaultShareMode:    defaultShareMode,
		})
}

// adjustAllocationEntriesWithOptions is the single implementation behind the
// adjustAllocationEntries* wrappers. It captures advisor configuration, derives
// pool and isolated quantities, and materializes the default-share residual.
func (p *DynamicPolicy) adjustAllocationEntriesWithOptions(
	ctx context.Context,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	opts adjustAllocationOptions,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	expectedRevision := opts.expectedRevision
	explicitRampUpFloor := opts.explicitRampUpFloor
	runCPUSetHandlers := opts.runCPUSetHandlers
	defaultShareMode := opts.defaultShareMode

	startTime := time.Now()
	general.Infof("called")
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()

	// Remove orphan non-resident pools from this adjustment's candidate before
	// deriving pool quantities and materializing the default-share residual.
	p.cleanPoolsFromPodEntries(entries)

	var poolsQuantityMap map[string]map[int]int
	sharedNUMABindingCPUIncrRatio := getSharedNUMABindingCPUIncrRatioWithConfig(dynamicConf)
	enableReclaim := false
	if dynamicConf != nil {
		enableReclaim = dynamicConf.EnableReclaim
	}
	advisorHealthy := p.config.enableCPUAdvisor && p.advisor.advisorMonitor != nil &&
		!cpuutil.AdvisorDegradation(p.advisor.advisorMonitor.GetHealthy(), enableReclaim)
	if dynamicConf != nil && dynamicConf.FillDefaultSharePoolWithNonReclaimCPUs &&
		defaultShareMode != defaultShareMaterializationRecovery && !advisorHealthy {
		return fmt.Errorf("default share residual quantity requires a healthy cpu advisor")
	}
	if advisorHealthy {
		poolsCPUSetMap, err := entries.GetFilteredPoolsCPUSetMap(state.IsResidentPool, commonstate.IsSystemPool)
		if err != nil {
			return fmt.Errorf("GetFilteredPoolsCPUSetMap failed with error: %v", err)
		}
		poolsQuantityMap = machine.ParseCPUAssignmentQuantityMap(poolsCPUSetMap)
	} else {
		var err error
		poolsQuantityMap, err = state.GetSharedQuantityMapFromPodEntries(
			machineState.GetNUMAResourcePackagePinnedCPUSet(),
			entries,
			nil,
			p.getContainerRequestedCores,
			sharedNUMABindingCPUIncrRatio,
		)
		if err != nil {
			return fmt.Errorf("GetSharedQuantityMapFromPodEntries failed with error: %w", err)
		}
	}
	isolatedQuantityMap := state.GetIsolatedQuantityMapFromPodEntries(entries, nil, p.getContainerRequestedCores)

	err := p.adjustPoolsAndIsolatedEntriesWithRampUpFloorForModeAtRevision(
		ctx, poolsQuantityMap, isolatedQuantityMap, entries, machineState, persistCheckpoint,
		explicitRampUpFloor, runCPUSetHandlers, expectedRevision, defaultShareMode, dynamicConf)
	if err != nil {
		return fmt.Errorf("adjustpoolsandisolatedentries failed with error: %w", err)
	}

	return nil
}

// adjustPoolsAndIsolatedEntries works for the following steps
// 1. calculate pools and isolated cpusets according to expectant quantities
// 2. make reclaimed overlap with numa-binding
// 3. apply them to local state; precommit removes orphan pools before the CAS
func (p *DynamicPolicy) adjustPoolsAndIsolatedEntries(
	poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
) error {
	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
	return p.adjustPoolsAndIsolatedEntriesWithRampUpFloorForModeAtRevision(
		context.Background(), poolsQuantityMap, isolatedQuantityMap, entries, machineState, persistCheckpoint,
		machine.NewCPUSet(), true, p.state.GetRevision(), defaultShareMaterializationNormal, dynamicConf)
}

func (p *DynamicPolicy) adjustPoolsAndIsolatedEntriesWithRampUpFloorAtRevision(
	ctx context.Context,
	poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	explicitRampUpFloor machine.CPUSet,
	runCPUSetHandlers bool,
	expectedRevision uint64,
) error {
	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
	return p.adjustPoolsAndIsolatedEntriesWithRampUpFloorForModeAtRevision(
		ctx, poolsQuantityMap, isolatedQuantityMap, entries, machineState,
		persistCheckpoint, explicitRampUpFloor, runCPUSetHandlers,
		expectedRevision, defaultShareMaterializationNormal, dynamicConf)
}

// adjustPoolsAndIsolatedEntriesWithRampUpFloorForModeAtRevision keeps the
// shared NUMA-binding ratio and its hard-partition floor on one immutable
// dynamicConfig captured at the outer attempt entry. The manager swaps whole
// *Configuration pointers atomically and never mutates a published object, so
// this single pointer stays valid and consistent for the whole attempt;
// re-reading live between decisions could size the pool under one generation
// and reserve reclaim capacity under another.
func (p *DynamicPolicy) adjustPoolsAndIsolatedEntriesWithRampUpFloorForModeAtRevision(
	ctx context.Context,
	poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	explicitRampUpFloor machine.CPUSet,
	runCPUSetHandlers bool,
	expectedRevision uint64,
	defaultShareMode defaultShareMaterializationMode,
	dynamicConf *dynamicconfig.Configuration,
) error {
	rampUpReclaimFloor := explicitRampUpFloor.Clone()
	hardPartitionEnabled := isRampUpReclaimHardPartitionEnabledWithConfig(dynamicConf)
	if hardPartitionEnabled && rampUpReclaimFloor.IsEmpty() {
		var err error
		// Pool adjustment admits no new allocation, so the ramp-up reclaim
		// floor follows the domains already recorded in the candidate entries.
		entriesRampUpDomains, dErr := p.activeRampUpDomainsFromEntries(entries)
		if dErr != nil {
			return fmt.Errorf("resolve active ramp-up domains before allocating pools: %w", dErr)
		}
		rampUpReclaimFloor, err = p.deriveRampUpReclaimFloorForModeWithDynamicConfig(
			machineState, entries, entriesRampUpDomains,
			p.state.GetDisableDedicatedCoresOverlapReclaimedCores(), dynamicConf)
		if err != nil {
			return fmt.Errorf("derive reclaim floor before allocating pools failed: %w", err)
		}
	}

	availableCPUs := machineState.GetFilteredAvailableCPUSet(p.config.reservedCPUs, nil,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicatedNUMABindingNUMAExclusive))

	// rpPinnedCPUSet contains the pinned CPU sets for resource packages
	rpPinnedCPUSet := machineState.GetResourcePackagePinnedCPUSet()

	// Subtract CPUs that cannot be assigned to user containers. Check both the
	// current entries and canonical state so default-share residual accounting
	// stays aligned with the advisor quantity when this snapshot misses system
	// or forbidden pools.
	notAllocatablePoolCPUs := p.getNotAllocatablePoolCPUs(entries)
	availableCPUs = availableCPUs.Difference(notAllocatablePoolCPUs)
	hardPartitionWithExplicitFloor := hardPartitionEnabled && !rampUpReclaimFloor.IsEmpty()
	if hardPartitionWithExplicitFloor {
		availableCPUs = availableCPUs.Difference(rampUpReclaimFloor)
	}

	reclaimOverlapShareRatio, err := p.getReclaimOverlapShareRatio(entries)
	if err != nil {
		return fmt.Errorf("reclaimOverlapShareRatio failed with error: %v", err)
	}

	general.Infof("poolsQuantityMap: %#v, isolatedQuantityMap: %#v, rpPinnedCPUSet: %v, availableCPUs: %v, reclaimOverlapShareRatio: %#v",
		poolsQuantityMap, isolatedQuantityMap, rpPinnedCPUSet, availableCPUs, reclaimOverlapShareRatio)

	// When FillDefaultSharePoolWithNonReclaimCPUs is enabled, the default
	// non-NUMA-binding share pool must not participate in the normal pool
	// allocation path. Instead we move it out of the quantity map (on a deep
	// copy so the caller's map is untouched), allocate every other fixed pool
	// first, and later materialize the default share pool with residual CPUs in
	// applyPoolsAndIsolatedInfo.
	// fixedPoolsQuantityMap remains a map of final pool targets. When an
	// explicit hard floor has already been removed from availableCPUs, only the
	// reclaim quantity not satisfied by that floor may enter the allocator;
	// otherwise allocating the full advised quantity and unioning the floor later
	// double-counts reclaim and can consume a near-machine dedicated allocation.
	fixedPoolsQuantityMap := copyPoolQuantityMap(poolsQuantityMap)
	if hardPartitionWithExplicitFloor {
		subtractReclaimFloorFromQuantityMap(
			fixedPoolsQuantityMap, rampUpReclaimFloor, p.machine.machineInfo.CPUTopology)
	}
	defaultSharePlan := defaultShareMaterializationPlan{mode: defaultShareMode}
	if dynamicConf != nil && dynamicConf.FillDefaultSharePoolWithNonReclaimCPUs {
		defaultSharePlan.enabled = true
		quantityByNUMA, ok := fixedPoolsQuantityMap[commonstate.PoolNameShare]
		if !ok {
			// A missing default share quantity is a legitimate zero-quantity
			// end-state: on a fully dedicated numa-exclusive node SysAdvisor
			// publishes no default share pool. Honor the advisor's zero and let
			// finalizeDefaultShareEntry prune any stale share entry, keeping
			// symmetry with the advisor-driven path.
			defaultSharePlan.advisedQuantity = 0
			defaultSharePlan.deriveUpperBoundFromEligible = false
		} else {
			if len(quantityByNUMA) != 1 {
				return fmt.Errorf("default share quantity map must contain only faked numa id")
			}
			quantity, ok := quantityByNUMA[commonstate.FakedNUMAID]
			if !ok {
				return fmt.Errorf("default share quantity map must contain only faked numa id")
			}
			defaultSharePlan.advisedQuantity = quantity
			defaultSharePlan.deriveUpperBoundFromEligible = true
			delete(fixedPoolsQuantityMap, commonstate.PoolNameShare)
		}
	}

	poolsCPUSet, isolatedCPUSet, err := p.groupAndAllocatePoolsWithPlacement(
		fixedPoolsQuantityMap, isolatedQuantityMap, availableCPUs,
		rpPinnedCPUSet, reclaimOverlapShareRatio, dynamicConf,
		entries, machineState.GetNUMAResourcePackagePinnedCPUSet(), rampUpReclaimFloor)
	if err != nil {
		return fmt.Errorf("groupAndAllocatePools failed with error: %v", err)
	}
	if hardPartitionWithExplicitFloor {
		if err := p.validateOwnedPoolsQuantity(
			poolsQuantityMap,
			poolsCPUSet,
			entries,
			machineState.GetNUMAResourcePackagePinnedCPUSet(),
		); err != nil {
			return err
		}
	}

	general.Infof("poolsCPUSet: %v, isolatedCPUSet: %v", poolsCPUSet, isolatedCPUSet)

	if hardPartitionWithExplicitFloor {
		poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(rampUpReclaimFloor)
	}
	err = p.reclaimOverlapNUMABinding(poolsCPUSet, entries, dynamicConf)
	if err != nil {
		return fmt.Errorf("reclaimOverlapNUMABinding failed with error: %v", err)
	}

	err = p.applyPoolsAndIsolatedInfo(poolsCPUSet, isolatedCPUSet, entries,
		machineState, state.GetSharedBindingNUMAsFromQuantityMap(poolsQuantityMap), persistCheckpoint, rampUpReclaimFloor,
		defaultSharePlan, expectedRevision, dynamicConf)
	if err != nil {
		return fmt.Errorf("applyPoolsAndIsolatedInfo failed with error: %w", err)
	}

	if runCPUSetHandlers {
		ctx, cancel := context.WithTimeout(ctx, cpuSetAdjustmentHandlerTimeout(p.config.conf))
		defer cancel()
		if err := p.runCPUSetAdjustmentHandlers(ctx, dynamicpolicyutil.CPUSetAdjustmentModeAdmission); err != nil {
			return fmt.Errorf("runCPUSetAdjustmentHandlers failed with error: %w", err)
		}
	}

	return nil
}

func (p *DynamicPolicy) validateOwnedPoolsQuantity(
	poolsQuantityMap map[string]map[int]int,
	poolsCPUSet map[string]machine.CPUSet,
	entries state.PodEntries,
	numaResourcePackagePinnedCPUSet map[int]map[string]machine.CPUSet,
) error {
	ownedPools := make(map[string]struct{})
	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}
			if !allocationInfo.CheckSharedNUMABinding() {
				continue
			}
			poolName, _, err := state.GetCanonicalSharedNUMABindingPoolKey(
				numaResourcePackagePinnedCPUSet, allocationInfo)
			if err != nil {
				return fmt.Errorf("get canonical shared numa-binding pool key for %s/%s/%s failed: %s",
					strings.ToLower(allocationInfo.PodNamespace),
					strings.ToLower(allocationInfo.PodName),
					strings.ToLower(allocationInfo.ContainerName),
					strings.ToLower(err.Error()))
			}
			if poolName != commonstate.EmptyOwnerPoolName {
				ownedPools[poolName] = struct{}{}
			}
		}
	}

	for poolName, numaQuantities := range poolsQuantityMap {
		if _, ok := ownedPools[poolName]; !ok {
			continue
		}

		requested := 0
		for _, quantity := range numaQuantities {
			if quantity > 0 {
				requested += quantity
			}
		}
		if requested == 0 {
			continue
		}

		allocated := poolsCPUSet[poolName]
		for numaID, quantity := range numaQuantities {
			if numaID == commonstate.FakedNUMAID || quantity <= 0 {
				continue
			}
			numaAllocated := allocated.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID)).Size()
			if numaAllocated < quantity {
				return fmt.Errorf("insufficient capacity for owned pool %q in numa %d: requested %d cpus, allocated %d",
					strings.ToLower(poolName), numaID, quantity, numaAllocated)
			}
		}
		if allocated.Size() < requested {
			return fmt.Errorf("insufficient capacity for owned pool %q: requested %d cpus, allocated %d",
				strings.ToLower(poolName), requested, allocated.Size())
		}
	}
	return nil
}

func (p *DynamicPolicy) getNotAllocatablePoolCPUs(entries state.PodEntries) machine.CPUSet {
	notAllocatablePoolCPUs := state.GetUnitedPoolsCPUs(entries, state.IsForbiddenPool, commonstate.IsSystemPool)
	if p == nil || p.state == nil {
		return notAllocatablePoolCPUs
	}

	return notAllocatablePoolCPUs.Union(
		state.GetUnitedPoolsCPUs(p.state.GetPodEntries(), state.IsForbiddenPool, commonstate.IsSystemPool),
	)
}

func (p *DynamicPolicy) groupAndAllocatePoolsWithPlacement(
	poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int,
	availableCPUs machine.CPUSet,
	rpPinnedCPUSet map[string]machine.CPUSet,
	reclaimOverlapShareRatio map[string]float64,
	dynamicConf *dynamicconfig.Configuration,
	entries state.PodEntries,
	numaResourcePackagePinnedCPUSet map[int]map[string]machine.CPUSet,
	rampUpReclaimFloors ...machine.CPUSet,
) (map[string]machine.CPUSet, map[string]map[string]machine.CPUSet, error) {
	rampUpReclaimFloor := machine.NewCPUSet()
	if len(rampUpReclaimFloors) > 0 {
		rampUpReclaimFloor = rampUpReclaimFloors[0].Clone()
	}
	// 1. Separate pools into pinned and common
	pinnedPoolsQuantityMap := make(map[string]map[int]int)
	commonPoolsQuantityMap := make(map[string]map[int]int)
	pinnedCPUSets := machine.NewCPUSet()

	// Accumulate all pinned cpusets from resource packages
	for _, cset := range rpPinnedCPUSet {
		pinnedCPUSets = pinnedCPUSets.Union(cset)
	}

	for poolName, quantityMap := range poolsQuantityMap {
		_, pkgName := rputil.UnwrapOwnerPoolName(poolName)
		if pkgName == "" {
			commonPoolsQuantityMap[poolName] = quantityMap
			continue
		}
		if rpPinnedCPUSet[pkgName].IsEmpty() {
			return nil, nil, fmt.Errorf("resource-package pool %q has no pinned cpuset for package %q", poolName, pkgName)
		}
		pinnedPoolsQuantityMap[poolName] = quantityMap
	}

	// 2. Calculate common available CPUs
	// For pools without pinned cpuset, availableCPUs needs to deduct allocated pinned cpuset
	commonAvailableCPUs := availableCPUs.Difference(pinnedCPUSets)

	// 3. Process Pinned Pools
	poolsCPUSet := make(map[string]machine.CPUSet)

	// Resource-package pools are canonical NUMA-binding pools. Keep each package
	// inside its own pinned CPUSet, but run only the lightweight NUMA-binding
	// allocator for that package instead of the full common-pool pipeline.
	pinnedPoolsByPackage := make(map[string]map[string]map[int]int)
	for poolName, quantityMap := range pinnedPoolsQuantityMap {
		_, pkgName := rputil.UnwrapOwnerPoolName(poolName)
		if pinnedPoolsByPackage[pkgName] == nil {
			pinnedPoolsByPackage[pkgName] = make(map[string]map[int]int)
		}
		pinnedPoolsByPackage[pkgName][poolName] = quantityMap
	}
	for pkgName, packagePools := range pinnedPoolsByPackage {
		packageAvailable := availableCPUs.Intersection(rpPinnedCPUSet[pkgName])
		bindingPools := make(map[string]map[int]int)
		nonBindingQuantities := make(map[string]int)
		for poolName, quantityMap := range packagePools {
			if len(quantityMap) > 1 {
				return nil, nil, fmt.Errorf("resource-package pool %q crosses NUMAs: %+v", poolName, quantityMap)
			}
			for numaID, quantity := range quantityMap {
				if quantity <= 0 {
					continue
				}
				if numaID == commonstate.FakedNUMAID {
					nonBindingQuantities[poolName] = quantity
				} else {
					bindingPools[poolName] = quantityMap
				}
			}
		}
		if len(bindingPools) > 0 {
			left, err := p.generateNUMABindingPoolsCPUSetInPlace(
				poolsCPUSet, bindingPools, packageAvailable, dynamicConf)
			if err != nil {
				return nil, nil, fmt.Errorf("allocate resource-package %q NUMA pools failed with error: %v", pkgName, err)
			}
			packageAvailable = left
		}
		if len(nonBindingQuantities) > 0 {
			totalQuantity := general.SumUpMapValues(nonBindingQuantities)
			var err error
			if dynamicConf != nil && dynamicConf.EnableReclaim &&
				!p.state.GetAllowSharedCoresOverlapReclaimedCores() &&
				totalQuantity <= packageAvailable.Size() {
				_, err = p.takeCPUsForPoolsInPlace(
					nonBindingQuantities, poolsCPUSet, packageAvailable)
			} else {
				_, err = p.generateProportionalPoolsCPUSetInPlace(
					nonBindingQuantities, poolsCPUSet, packageAvailable)
			}
			if err != nil {
				return nil, nil, fmt.Errorf("allocate resource-package %q non-NUMA pools failed with error: %v", pkgName, err)
			}
		}
	}

	// 4. Process Common Pools
	// Pass rpPinnedCPUSet to generatePoolsAndIsolation to handle pinned resources
	general.Infof("commonPoolsQuantityMap: %#v, commonAvailableCPUs: %v", commonPoolsQuantityMap, commonAvailableCPUs)
	placement := buildPoolPlacementSnapshot(
		entries, numaResourcePackagePinnedCPUSet,
		commonPoolsQuantityMap, isolatedQuantityMap, commonAvailableCPUs, true)
	cPools, cIso, err := p.generatePoolsAndIsolation(
		commonPoolsQuantityMap, isolatedQuantityMap, commonAvailableCPUs,
		reclaimOverlapShareRatio,
		generatePoolsAndIsolationOptions{
			rampUpReclaimFloor:    rampUpReclaimFloor,
			enforceReclaimMinimum: true,
			dynamicConf:           dynamicConf,
			placement:             placement,
		})
	if err != nil {
		return nil, nil, fmt.Errorf("generatePoolsAndIsolation failed with error: %v", err)
	}

	for k, v := range cPools {
		poolsCPUSet[k] = v
	}
	isolatedCPUSet := cIso

	return poolsCPUSet, isolatedCPUSet, nil
}

// reclaimOverlapNUMABinding unions calculated reclaim pool in empty NUMAs
// with the intersection of previous reclaim pool and non-ramp-up dedicated_cores numa_binding containers
func (p *DynamicPolicy) reclaimOverlapNUMABinding(poolsCPUSet map[string]machine.CPUSet, entries state.PodEntries, dynamicConf *dynamicconfig.Configuration) error {
	// reclaimOverlapNUMABinding only works with cpu advisor and reclaim enabled
	if !(p.config.enableCPUAdvisor && dynamicConf != nil && dynamicConf.EnableReclaim) {
		return nil
	}

	reclaimPool, ok := entries[commonstate.PoolNameReclaim]
	if !ok {
		return fmt.Errorf("reclaim pool misses in current entries")
	}
	fakeEntry, ok := reclaimPool[commonstate.FakedContainerName]
	if !ok || fakeEntry.AllocationResult.IsEmpty() {
		return fmt.Errorf("reclaim pool misses in current entries")
	}

	curReclaimCPUSet := fakeEntry.AllocationResult.Clone()
	nonOverlapReclaimCPUSet := poolsCPUSet[commonstate.PoolNameReclaim].Clone()
	general.Infof("curReclaimCPUSet: %s", curReclaimCPUSet.String())

	if !p.state.GetDisableDedicatedCoresOverlapReclaimedCores() {
		for _, containerEntries := range entries {
			if containerEntries.IsPoolEntry() {
				continue
			}

			for _, allocationInfo := range containerEntries {
				if !(allocationInfo != nil && allocationInfo.CheckDedicatedNUMABinding() && allocationInfo.CheckMainContainer()) {
					continue
				} else if allocationInfo.RampUp {
					general.Infof("dedicated numa_binding pod: %s/%s container: %s is in ramp up, not to overlap reclaim pool with it",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
					continue
				}

				poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(curReclaimCPUSet.Intersection(allocationInfo.AllocationResult))
			}
		}
	}

	if poolsCPUSet[commonstate.PoolNameReclaim].IsEmpty() {
		return fmt.Errorf("reclaim pool is empty after overlapping with dedicated_cores numa_binding containers")
	}

	general.Infof("nonOverlapReclaimCPUSet: %s, finalReclaimCPUSet: %s", nonOverlapReclaimCPUSet.String(), poolsCPUSet[commonstate.PoolNameReclaim].String())
	return nil
}

// copyPoolQuantityMap deep-copies the pools quantity map so that the caller's
// map is never mutated (e.g. when we delete the default share pool for the
// residual-backfill path).
func copyPoolQuantityMap(in map[string]map[int]int) map[string]map[int]int {
	out := make(map[string]map[int]int, len(in))
	for poolName, byNUMA := range in {
		copied := make(map[int]int, len(byNUMA))
		for numaID, quantity := range byNUMA {
			copied[numaID] = quantity
		}
		out[poolName] = copied
	}
	return out
}

// subtractReclaimFloorFromQuantityMap converts the final reclaim target into the
// remainder the pool allocator still has to select after the explicit hard floor
// has been removed from its available CPU set. It mutates only the caller-owned
// copy. FakeNUMA targets are global; explicit NUMA targets consume only the floor
// already selected in that NUMA.
func subtractReclaimFloorFromQuantityMap(
	quantities map[string]map[int]int,
	floor machine.CPUSet,
	topology *machine.CPUTopology,
) {
	byNUMA, ok := quantities[commonstate.PoolNameReclaim]
	if !ok || floor.IsEmpty() || topology == nil {
		return
	}
	for numaID, quantity := range byNUMA {
		satisfied := floor.Size()
		if numaID != commonstate.FakedNUMAID {
			satisfied = floor.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID)).Size()
		}
		remaining := quantity - satisfied
		if remaining < 0 {
			remaining = 0
		}
		byNUMA[numaID] = remaining
	}
}

type defaultShareMaterializationPlan struct {
	enabled                      bool
	advisedQuantity              int
	deriveUpperBoundFromEligible bool
	eligibleCPUSet               machine.CPUSet
	mode                         defaultShareMaterializationMode
}

type defaultShareMaterializationMode uint8

const (
	defaultShareMaterializationNormal defaultShareMaterializationMode = iota
	defaultShareMaterializationRecovery
)

// buildDefaultShareEligibleCPUSet derives the default-share source of truth
// from the complete physical topology and the entries being finalized. It must
// not start from NUMANodeState.DefaultCPUSet: that field can still describe the
// old placement while an exclusive DNB container is migrating between NUMAs.
func (p *DynamicPolicy) buildDefaultShareEligibleCPUSet(
	finalizedEntries state.PodEntries,
	machineState state.NUMANodeMap,
	rampUpReclaimFloor machine.CPUSet,
) machine.CPUSet {
	eligible := p.machine.machineInfo.CPUDetails.CPUs().Difference(p.config.reservedCPUs)

	exclusiveNUMAs := sets.NewInt()
	for _, containerEntries := range finalizedEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil || !allocationInfo.CheckDedicatedNUMABindingNUMAExclusive() {
				continue
			}
			for numaID := range allocationInfo.TopologyAwareAssignments {
				exclusiveNUMAs.Insert(numaID)
			}
			if len(allocationInfo.TopologyAwareAssignments) == 0 {
				for _, numaID := range p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceNoSortInt() {
					if !allocationInfo.AllocationResult.Intersection(
						p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID)).IsEmpty() {
						exclusiveNUMAs.Insert(numaID)
					}
				}
			}
		}
	}
	eligible = eligible.Difference(
		p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(exclusiveNUMAs.UnsortedList()...))
	eligible = eligible.Difference(
		state.GetUnitedPoolsCPUs(finalizedEntries, state.IsForbiddenPool, commonstate.IsSystemPool))
	for _, pinned := range machineState.GetResourcePackagePinnedCPUSet() {
		eligible = eligible.Difference(pinned)
	}
	return eligible.Difference(rampUpReclaimFloor)
}

// globalDomainActiveRampUpCPUSet returns the CPUs held by ramp-up shared
// allocations in the global (non-NUMA-scoped) domain. NUMA-binding ramp-up
// allocations are excluded because their CPUs belong to a fixed per-NUMA pool,
// not to the default share residual that this set is used to exempt.
func globalDomainActiveRampUpCPUSet(entries state.PodEntries) machine.CPUSet {
	result := machine.NewCPUSet()
	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil || !allocationInfo.RampUp || !allocationInfo.CheckShared() ||
				allocationInfo.CheckNUMABinding() {
				continue
			}
			result = result.Union(allocationInfo.AllocationResult)
		}
	}
	return result
}

// unionPoolCPUSet unions all pool cpusets except the excluded pool.
func unionPoolCPUSet(pools map[string]machine.CPUSet, excludedPool string) machine.CPUSet {
	result := machine.NewCPUSet()
	for poolName, cpus := range pools {
		if poolName == excludedPool {
			continue
		}
		result = result.Union(cpus)
	}
	return result
}

// unionIsolatedCPUSet unions all isolated container cpusets.
func unionIsolatedCPUSet(isolated map[string]map[string]machine.CPUSet) machine.CPUSet {
	result := machine.NewCPUSet()
	for _, containers := range isolated {
		for _, cpus := range containers {
			result = result.Union(cpus)
		}
	}
	return result
}

func dedicatedCPUSetFromPodEntries(entries state.PodEntries) map[string]map[string]machine.CPUSet {
	dedicated := make(map[string]map[string]machine.CPUSet)
	for podUID, containers := range entries {
		if containers.IsPoolEntry() {
			continue
		}
		for containerName, allocationInfo := range containers {
			if allocationInfo == nil || allocationInfo.GetPoolName() != commonstate.PoolNameDedicated {
				continue
			}
			if dedicated[podUID] == nil {
				dedicated[podUID] = make(map[string]machine.CPUSet)
			}
			dedicated[podUID][containerName] = allocationInfo.AllocationResult.Clone()
		}
	}
	return dedicated
}

func supplementPodEntriesWithCurrentNonPool(pending, current state.PodEntries) state.PodEntries {
	merged := pending.Clone()
	if merged == nil {
		merged = make(state.PodEntries)
	}
	for podUID, containerEntries := range current {
		if containerEntries.IsPoolEntry() {
			continue
		}
		if merged[podUID] == nil {
			merged[podUID] = make(state.ContainerEntries)
		}
		for containerName, allocationInfo := range containerEntries {
			if _, ok := merged[podUID][containerName]; ok {
				continue
			}
			if allocationInfo == nil {
				merged[podUID][containerName] = nil
			} else {
				merged[podUID][containerName] = allocationInfo.Clone()
			}
		}
	}
	return merged
}

func hasLiveDefaultShareOwner(entries state.PodEntries) bool {
	for _, containers := range entries {
		if containers.IsPoolEntry() {
			continue
		}
		for _, allocationInfo := range containers {
			if allocationInfo != nil && !allocationInfo.RampUp &&
				allocationInfo.GetPoolName() == commonstate.PoolNameShare {
				return true
			}
		}
	}
	return false
}

// materializeDefaultShareCPUSet computes the residual cpuset for the default
// share pool by subtracting every fixed pool (except the default share pool
// itself) and every isolated container cpuset from availableCPUs, then fails
// closed when the expected quantity produced by SysAdvisor is smaller than the
// residual size.
//
// Mutual-exclusion premise: this residual backfill relies on
// FillDefaultSharePoolWithNonReclaimCPUs (the backfill gate) and
// AllowSharedCoresOverlapReclaimedCores being mutually exclusive. That
// mutual exclusion is enforced on the SysAdvisor side by
// validateDefaultShareBackfillConfig (assembler_common.go) at the quantity
// layer: when the gate is enabled but overlap is allowed it errors out and no
// default share quantity is produced. Therefore we deliberately do NOT
// re-validate the overlap flag here. Even if both flags were mis-enabled
// together, the residual size could exceed the expected quantity and the check
// below makes QRM fail closed (reject the allocation) rather than silently
// produce a wrong cpuset. A larger expected quantity is accepted because
// SysAdvisor publishes the allocatable upper bound while QRM materializes the
// exact current reclaim/fixed/isolated CPUSet union; the residual cpuset is the
// CPUSet-level source of truth.
func materializeDefaultShareCPUSet(expectedQuantity int, availableCPUs machine.CPUSet,
	pools map[string]machine.CPUSet, isolated map[string]map[string]machine.CPUSet,
) (machine.CPUSet, error) {
	return materializeDefaultShareCPUSetForMode(
		expectedQuantity, availableCPUs, pools, isolated, defaultShareMaterializationNormal)
}

func materializeDefaultShareCPUSetForMode(expectedQuantity int, availableCPUs machine.CPUSet,
	pools map[string]machine.CPUSet, isolated map[string]map[string]machine.CPUSet,
	mode defaultShareMaterializationMode,
) (machine.CPUSet, error) {
	fixed := unionPoolCPUSet(pools, commonstate.PoolNameShare).Union(unionIsolatedCPUSet(isolated))
	residual := availableCPUs.Difference(fixed)
	if residual.Size() > expectedQuantity && mode != defaultShareMaterializationRecovery {
		return machine.NewCPUSet(), &DefaultShareResidualQuantityError{
			AdvisedQuantity: expectedQuantity,
			ResidualSize:    residual.Size(),
			AvailableCPUs:   availableCPUs,
			FixedCPUs:       fixed,
			ResidualCPUs:    residual,
		}
	}
	if residual.Size() < expectedQuantity {
		general.InfoS("default share residual cpuset shrank before advisor quantity",
			"expectedQuantity", expectedQuantity,
			"actualSize", residual.Size(),
			"availableCPUs", availableCPUs.String(),
			"fixedCPUs", fixed.String(),
			"residualCPUs", residual.String(),
		)
	}
	general.InfoS("default share residual cpuset allocated",
		"expectedQuantity", expectedQuantity,
		"actualSize", residual.Size(),
	)
	return residual, nil
}

// finalizeDefaultShareEntry re-derives the default share pool cpuset from the
// post-overlap / post-revise pool entries and installs it into newPodEntries.
// It reads each non-share pool's AllocationResult directly from newPodEntries
// so that any adjustment made by reclaimOverlapNUMABinding / reviseReclaimPool /
// explicitRampUpFloor is naturally reflected in the residual. Normal updates
// reject residuals above advice; rollback and pod-removal recovery preserve the
// live residual until SysAdvisor catches up.
func (p *DynamicPolicy) finalizeDefaultShareEntry(
	newPodEntries state.PodEntries,
	ownershipEntries state.PodEntries,
	expectedQuantity int, candidate machine.CPUSet,
) error {
	return p.finalizeDefaultShareEntryForMode(
		newPodEntries, ownershipEntries, expectedQuantity, candidate,
		defaultShareMaterializationNormal)
}

func (p *DynamicPolicy) finalizeDefaultShareEntryForMode(
	newPodEntries state.PodEntries,
	ownershipEntries state.PodEntries,
	expectedQuantity int,
	candidate machine.CPUSet,
	mode defaultShareMaterializationMode,
) error {
	finalPools := make(map[string]machine.CPUSet)
	for poolName, entries := range newPodEntries {
		if !entries.IsPoolEntry() || poolName == commonstate.PoolNameShare {
			continue
		}
		finalPools[poolName] = entries[commonstate.FakedContainerName].AllocationResult
	}
	share, err := materializeDefaultShareCPUSetForMode(
		expectedQuantity, candidate, finalPools,
		dedicatedCPUSetFromPodEntries(ownershipEntries), mode)
	if err != nil {
		return err
	}
	if share.IsEmpty() {
		if !hasLiveDefaultShareOwner(ownershipEntries) {
			general.InfoS("prune empty ownerless default share residual",
				"expectedQuantity", expectedQuantity)
			delete(newPodEntries, commonstate.PoolNameShare)
			return nil
		}
		return fmt.Errorf("default share residual is empty with live owner")
	}
	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, share)
	if err != nil {
		return fmt.Errorf("calculate default share topology assignments: %w", err)
	}
	newPodEntries[commonstate.PoolNameShare] = state.ContainerEntries{
		commonstate.FakedContainerName: {
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
			AllocationResult:                 share.Clone(),
			OriginalAllocationResult:         share.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		},
	}
	return nil
}

// applyPoolsAndIsolatedInfo generates the latest checkpoint by pools and isolated cpusets calculation results.
// 1. construct entries for isolated containers (probably be dedicated_cores not numa_binding )
// 2. construct entries for all pools
// 3. construct entries for shared_cores, reclaimed_cores, numa_binding dedicated_cores containers
// buildIsolatedPodEntries constructs entries for dedicated-cores (without NUMA
// binding) containers from the isolated cpuset plan, and returns them together
// with the union of isolated CPUs. Containers that are absent from the current
// checkpoint or not eligible isolation targets are skipped.
// buildPoolEntries writes a faked container entry for every pool cpuset into
// newPodEntries, computing NUMA-aware assignments for each pool.
func (p *DynamicPolicy) buildPoolEntries(
	newPodEntries state.PodEntries,
	poolsCPUSet map[string]machine.CPUSet,
	curEntries state.PodEntries,
) error {
	for poolName, cset := range poolsCPUSet {
		general.Infof("try to apply pool %s: %s", poolName, cset.String())
		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, cset)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, result cpuset: %s, error: %v",
				poolName, cset.String(), err)
		}

		allocationInfo := curEntries[poolName][commonstate.FakedContainerName]
		if allocationInfo != nil {
			general.Infof("pool: %s allocation result transform from %s(size: %d) to %s(size: %d)",
				poolName, allocationInfo.AllocationResult.String(), allocationInfo.AllocationResult.Size(),
				cset.String(), cset.Size())
		}

		if newPodEntries[poolName] == nil {
			newPodEntries[poolName] = make(state.ContainerEntries)
		}
		newPodEntries[poolName][commonstate.FakedContainerName] = &state.AllocationInfo{
			AllocationMeta:                   derivePoolOwnerAllocationMeta(poolName, curEntries),
			AllocationResult:                 cset.Clone(),
			OriginalAllocationResult:         cset.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		}
	}
	return nil
}

// derivePoolOwnerAllocationMeta materializes owner-accurate metadata for a faked
// pool entry. A faked entry that drops its QoS level and numa_binding declaration
// fails CheckSharedNUMABinding, so GetFilteredPoolsCPUSetMap buckets it under
// FakedNUMAID; the next same-pool member then trips the one-pool-one-NUMA
// invariant (map[-1:1 0:1]). This is what kept ramp-up shared-NUMA-binding (SNB)
// pools from admitting a second member, because the ramp-up path never stamps the
// pool entry the way the settled path does.
//
// The decision is driven only by the pool's committed entry and resident members,
// never by the pool name alone:
//   - If at least one resident member resolves to poolName, the faked entry is
//     stamped with shared_cores + numa_binding=true only when ALL such members are
//     consistently SNB. Inconsistent (mixed) membership keeps the generic meta, so
//     the cross-NUMA invariant still fails closed instead of silently relabeling.
//   - If no resident member resolves to poolName on this materialization (e.g. a
//     reload re-materializing the pool before its members are inspected), a
//     previously committed binding identity is preserved so a restart / checkpoint
//     reload does not drop the pool's NUMA classification.
//   - Otherwise the generic pool meta is returned unchanged.
func derivePoolOwnerAllocationMeta(poolName string, curEntries state.PodEntries) commonstate.AllocationMeta {
	generic := commonstate.GenerateGenericPoolAllocationMeta(poolName)

	allSNB, hasMember := poolMembersConsistentlySharedNUMABinding(poolName, curEntries)
	switch {
	case hasMember && allSNB:
		generic.QoSLevel = apiconsts.PodAnnotationQoSLevelSharedCores
		if generic.Annotations == nil {
			generic.Annotations = make(map[string]string)
		}
		generic.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] = apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable
		return generic
	case hasMember:
		// Mixed membership: stay generic and let the invariant fail closed.
		return generic
	default:
		// No resident member on this pass; preserve a previously committed identity.
		if existing := curEntries[poolName][commonstate.FakedContainerName]; existing != nil && existing.CheckSharedNUMABinding() {
			cloned := existing.AllocationMeta.Clone()
			cloned.PodUid = poolName
			cloned.OwnerPoolName = poolName
			return *cloned
		}
		return generic
	}
}

// poolMembersConsistentlySharedNUMABinding reports whether curEntries contains at
// least one resident member resolving to poolName (hasMember) and whether every
// such member is shared-NUMA-binding (allSNB). The first non-SNB resolver forces
// allSNB to false so the caller fails closed on mixed membership.
func poolMembersConsistentlySharedNUMABinding(poolName string, curEntries state.PodEntries) (allSNB bool, hasMember bool) {
	for _, containerEntries := range curEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, info := range containerEntries {
			if info == nil {
				continue
			}
			if !allocationInfoResolvesToPool(info, poolName) {
				continue
			}
			hasMember = true
			if !info.CheckSharedNUMABinding() {
				return false, true
			}
		}
	}
	return true, hasMember
}

// allocationInfoResolvesToPool reports whether a resident member belongs to the
// given pool. It covers both settled members (OwnerPoolName materialized) and
// ramp-up members whose owner pool is left empty (resolved through the canonical
// NUMA-binding pool name). Non-SNB members are only matched by an explicit owner
// pool name, so an ordinary shared container never pulls a binding pool onto itself.
func allocationInfoResolvesToPool(info *state.AllocationInfo, poolName string) bool {
	if info.OwnerPoolName == poolName {
		return true
	}
	if !info.CheckSharedNUMABinding() {
		return false
	}
	resolved, err := info.GetSpecifiedNUMABindingPoolName()
	if err != nil {
		return false
	}
	return resolved == poolName
}

func (p *DynamicPolicy) buildIsolatedPodEntries(
	isolatedCPUSet map[string]map[string]machine.CPUSet,
	curEntries state.PodEntries,
) (state.PodEntries, machine.CPUSet) {
	newPodEntries := make(state.PodEntries)
	unionDedicatedIsolatedCPUSet := machine.NewCPUSet()

	for podUID, containerEntries := range isolatedCPUSet {
		for containerName, isolatedCPUs := range containerEntries {
			allocationInfo := curEntries[podUID][containerName]
			if allocationInfo == nil {
				general.Errorf("isolated pod: %s, container: %s without entry in current checkpoint", podUID, containerName)
				continue
			} else if !allocationInfo.CheckDedicated() || allocationInfo.CheckNUMABinding() {
				general.Errorf("isolated pod: %s, container: %s isn't dedicated_cores without NUMA binding", podUID, containerName)
				continue
			}

			topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, isolatedCPUs)
			if err != nil {
				general.ErrorS(err, "Unable to calculate topologyAwareAssignments",
					"podNamespace", allocationInfo.PodNamespace,
					"podName", allocationInfo.PodName,
					"containerName", allocationInfo.ContainerName,
					"result cpuset", isolatedCPUs.String())
				continue
			}

			general.InfoS("isolate info",
				"podNamespace", allocationInfo.PodNamespace,
				"podName", allocationInfo.PodName,
				"containerName", allocationInfo.ContainerName,
				"result cpuset", isolatedCPUs.String(),
				"result cpuset size", isolatedCPUs.Size(),
				"qosLevel", allocationInfo.QoSLevel)

			if newPodEntries[podUID] == nil {
				newPodEntries[podUID] = make(state.ContainerEntries)
			}

			newPodEntries[podUID][containerName] = allocationInfo.Clone()
			newPodEntries[podUID][containerName].OwnerPoolName = commonstate.PoolNameDedicated
			newPodEntries[podUID][containerName].AllocationResult = isolatedCPUs.Clone()
			newPodEntries[podUID][containerName].OriginalAllocationResult = isolatedCPUs.Clone()
			newPodEntries[podUID][containerName].TopologyAwareAssignments = topologyAwareAssignments
			newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)

			unionDedicatedIsolatedCPUSet = unionDedicatedIsolatedCPUSet.Union(isolatedCPUs)
		}
	}
	return newPodEntries, unionDedicatedIsolatedCPUSet
}

func (p *DynamicPolicy) applyPoolsAndIsolatedInfo(poolsCPUSet map[string]machine.CPUSet,
	isolatedCPUSet map[string]map[string]machine.CPUSet, curEntries state.PodEntries,
	machineState state.NUMANodeMap, sharedBindingNUMAs sets.Int, persistCheckpoint bool,
	explicitRampUpFloor machine.CPUSet,
	defaultSharePlan defaultShareMaterializationPlan,
	stateRevision uint64,
	dynamicConf *dynamicconfig.Configuration,
) error {
	allowSharedCoresOverlapReclaimedCores := p.state.GetAllowSharedCoresOverlapReclaimedCores()
	disableDedicatedCoresOverlapReclaimedCores := p.state.GetDisableDedicatedCoresOverlapReclaimedCores()
	// This attempt's immutable reclaim-fallback mode: enableReclaim from the
	// dynamicConf snapshot threaded into this apply, share/dedicated flags from
	// the committed state. It is built once here and reused by materialization,
	// revise and precommit validation so they observe the same policy.
	reclaimMode := reclaimFallbackMode{
		enableReclaim:           dynamicConf != nil && dynamicConf.EnableReclaim,
		allowSharedOverlap:      allowSharedCoresOverlapReclaimedCores,
		disableDedicatedOverlap: disableDedicatedCoresOverlapReclaimedCores,
	}
	// Calculate from this adjustment's candidate snapshot so materialization
	// and precommit validation observe the same RNB ownership.
	nonReclaimActualBindingNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))
	// 1. construct entries for isolated containers (probably be dedicated_cores not numa_binding)
	newPodEntries, unionDedicatedIsolatedCPUSet := p.buildIsolatedPodEntries(isolatedCPUSet, curEntries)

	// 2. construct entries for all pools
	rampUpReclaimFloor := explicitRampUpFloor.Clone()
	if !rampUpReclaimFloor.IsEmpty() {
		poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(rampUpReclaimFloor)
	}
	// Do not fail early on an empty reclaim pool here: buildPoolEntries must be able
	// to materialize an empty reclaim entry so reviseReclaimPool can repair it from
	// the eligible set. The terminal non-empty/legal check is enforced by the
	// existing precommit/final validation on the actual newEntries after all
	// mutations (default share, ramp-up, container, normalize), not by this early guard.

	if err := p.buildPoolEntries(newPodEntries, poolsCPUSet, curEntries); err != nil {
		return err
	}

	// revise reclaim pool size to avoid reclaimed_cores and numa_binding containers
	// in NUMAs without cpuset actual binding
	err := p.reviseReclaimPool(
		newPodEntries,
		nonReclaimActualBindingNUMAs,
		unionDedicatedIsolatedCPUSet,
		reclaimMode,
	)
	if err != nil {
		return err
	}

	// backfill the default non-NUMA-binding share pool with residual CPUs
	// after every fixed pool has been settled (post reclaimOverlapNUMABinding,
	// post reviseReclaimPool, post explicitRampUpFloor merge). This must happen
	// before rampUpCPUs computation and before any SetPodEntries/StoreState so
	// that a quantity mismatch fails closed without persisting a partial
	// checkpoint.
	if defaultSharePlan.enabled {
		eligibilityEntries := supplementPodEntriesWithCurrentNonPool(newPodEntries, curEntries)
		defaultSharePlan.eligibleCPUSet = p.buildDefaultShareEligibleCPUSet(
			eligibilityEntries, machineState, rampUpReclaimFloor)
		if defaultSharePlan.deriveUpperBoundFromEligible {
			defaultSharePlan.advisedQuantity = defaultSharePlan.eligibleCPUSet.Size()
		}
		if err := p.finalizeDefaultShareEntryForMode(
			newPodEntries, eligibilityEntries, defaultSharePlan.advisedQuantity,
			defaultSharePlan.eligibleCPUSet, defaultSharePlan.mode,
		); err != nil {
			return err
		}
	}

	sharedBindingNUMACPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(sharedBindingNUMAs.UnsortedList()...)
	notAllocatablePoolsCPUs := state.GetUnitedPoolsCPUs(newPodEntries, state.IsForbiddenPool, commonstate.IsSystemPool)
	// rampUpCPUs include reclaim pool in NUMAs without NUMA_binding cpus
	rampUpCPUs := machineState.GetFilteredAvailableCPUSet(p.config.reservedCPUs,
		nil,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicatedNUMABindingNUMAExclusive)).
		Difference(unionDedicatedIsolatedCPUSet).
		Difference(sharedBindingNUMACPUs).
		Difference(notAllocatablePoolsCPUs)
	rampUpCPUs = rampUpCPUs.Difference(rampUpReclaimFloor)

	rampUpCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, rampUpCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for rampUpCPUs, result cpuset: %s, error: %v",
			rampUpCPUs.String(), err)
	}

	// 3. construct entries for shared_cores, reclaimed_cores, numa_binding dedicated_cores containers
	for podUID, containerEntries := range curEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

	containerLoop:
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			}

			if newPodEntries[podUID][containerName] != nil {
				general.Infof("pod: %s/%s, container: %s, qosLevel: %s is isolated, ignore original allocationInfo",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)
				continue
			}

			if newPodEntries[podUID] == nil {
				newPodEntries[podUID] = make(state.ContainerEntries)
			}

			newPodEntries[podUID][containerName] = allocationInfo.Clone()
			// Old checkpoints may not carry RequestQuantity. Preserve a positive
			// persisted request; only the compatibility path needs a MetaServer
			// lookup. This also keeps an elastic physical allocation from
			// rewriting its declared request to the smaller actual cpuset.
			if newPodEntries[podUID][containerName].RequestQuantity <= 0 {
				newPodEntries[podUID][containerName].RequestQuantity = p.getContainerRequestedCores(allocationInfo)
			}
			switch allocationInfo.QoSLevel {
			case apiconsts.PodAnnotationQoSLevelDedicatedCores:
				newPodEntries[podUID][containerName].OwnerPoolName = allocationInfo.GetPoolName()

				// for numa_binding containers, we just clone checkpoint already exist
				if allocationInfo.CheckDedicatedNUMABinding() {
					continue containerLoop
				}

				// dedicated_cores without numa_binding is not isolated, we will try to isolate it in next adjustment.
				general.Warningf("pod: %s/%s, container: %s is dedicated_cores without numa_binding but not isolated, "+
					"we put it into fallback pool: %s temporary",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, rampUpCPUs.String())

				newPodEntries[podUID][containerName].OwnerPoolName = commonstate.PoolNameFallback
				newPodEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
				newPodEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
				newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
				newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)

			case apiconsts.PodAnnotationQoSLevelSystemCores:
				poolCPUSet, topologyAwareAssignments, err := p.getSystemPoolCPUSetAndNumaAwareAssignments(newPodEntries, allocationInfo)
				if err != nil {
					return fmt.Errorf("pod: %s/%s, container: %s is system_cores, "+
						"getSystemPoolCPUSetAndNumaAwareAssignments failed with error: %v",
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, err)
				}

				newPodEntries[podUID][containerName].AllocationResult = poolCPUSet
				newPodEntries[podUID][containerName].OriginalAllocationResult = poolCPUSet.Clone()
				newPodEntries[podUID][containerName].TopologyAwareAssignments = topologyAwareAssignments
				newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)

			case apiconsts.PodAnnotationQoSLevelSharedCores:
				var ownerPoolName string
				if allocationInfo.CheckSharedNUMABinding() {
					ownerPoolName = allocationInfo.GetOwnerPoolName()

					if ownerPoolName == commonstate.EmptyOwnerPoolName {
						var err error
						// why do we integrate GetOwnerPoolName + GetSpecifiedNUMABindingPoolName into GetPoolName for SharedNUMABinding containers?
						// it's because we reply on GetSpecifiedPoolName (in GetPoolName) when calling CheckNUMABindingAntiAffinity,
						// At that time, NUMA hint for the candidate container isn't confirmed, so we can't implement NUMA hint aware logic in GetSpecifiedPoolName.
						ownerPoolName, err = allocationInfo.GetSpecifiedNUMABindingPoolName()
						if err != nil {
							return fmt.Errorf("pod: %s/%s, container: %s is shared_cores with numa_binding, "+
								"GetSpecifiedNUMABindingPoolName failed with error: %v",
								allocationInfo.PodNamespace, allocationInfo.PodName,
								allocationInfo.ContainerName, err)
						}

						pkgName := allocationInfo.GetResourcePackageName()
						if pkgName != "" {
							numaSet, err := machine.Parse(allocationInfo.Annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint])
							if err != nil {
								return fmt.Errorf("parse numaHintStr: %s failed with error: %v",
									allocationInfo.Annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint], err)
							}

							if numaSet.Size() == 1 {
								targetNUMAID := numaSet.ToSliceNoSortInt()[0]
								if pinnedSets, ok := machineState.GetNUMAResourcePackagePinnedCPUSet()[targetNUMAID]; ok {
									if cpuSet, exists := pinnedSets[pkgName]; exists && cpuSet.Size() > 0 {
										ownerPoolName = rputil.WrapOwnerPoolName(ownerPoolName, pkgName)
									}
								}
							}
						}
					} // else already in a numa_binding share pool or isolated
				} else {
					ownerPoolName = allocationInfo.GetPoolName()
				}

				if allocationInfo.RampUp {
					if allocationInfo.CheckSharedNUMABinding() {
						snbRampUpCPUs, snbRampUpCPUsTopologyAwareAssignments, err := p.getSharedNUMABindingRampUpCPUSet(
							allocationInfo, machineState, unionDedicatedIsolatedCPUSet, notAllocatablePoolsCPUs, rampUpReclaimFloor,
							poolsCPUSet[commonstate.PoolNameReclaim])
						if err != nil {
							return err
						}
						general.Infof("pod: %s/%s container: %s is in SNB ramp up, set its allocation result from %s to SNB rampUpCPUs: %s",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
							allocationInfo.AllocationResult.String(), snbRampUpCPUs.String())

						newPodEntries[podUID][containerName].OwnerPoolName = commonstate.EmptyOwnerPoolName
						newPodEntries[podUID][containerName].AllocationResult = snbRampUpCPUs.Clone()
						newPodEntries[podUID][containerName].OriginalAllocationResult = snbRampUpCPUs.Clone()
						newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(snbRampUpCPUsTopologyAwareAssignments)
						newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(snbRampUpCPUsTopologyAwareAssignments)
					} else {
						general.Infof("pod: %s/%s container: %s is in ramp up, set its allocation result from %s to rampUpCPUs: %s",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
							allocationInfo.AllocationResult.String(), rampUpCPUs.String())

						newPodEntries[podUID][containerName].OwnerPoolName = commonstate.EmptyOwnerPoolName
						newPodEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
						newPodEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
						newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
						newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
					}
				} else {
					poolEntry, err := p.getAllocationPoolEntry(allocationInfo, ownerPoolName, newPodEntries)
					if err != nil {
						return err
					}

					general.Infof("put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
						ownerPoolName, allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

					if allocationInfo.CheckSharedNUMABinding() {
						poolEntry.QoSLevel = apiconsts.PodAnnotationQoSLevelSharedCores
						// set SharedNUMABinding declarations to pool entry containing SharedNUMABinding containers,
						// in order to differentiate them from non-binding share cores pools during GetFilteredPoolsCPUSetMap.
						poolEntry.Annotations = general.MergeMap(poolEntry.Annotations, map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						})
					}

					newPodEntries[podUID][containerName].OwnerPoolName = ownerPoolName
					newPodEntries[podUID][containerName].AllocationResult = poolEntry.AllocationResult.Clone()
					newPodEntries[podUID][containerName].OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
					newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
					newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
				}
			case apiconsts.PodAnnotationQoSLevelReclaimedCores:
				poolEntry, err := p.getAllocationPoolEntry(allocationInfo, allocationInfo.OwnerPoolName, newPodEntries)
				if err != nil {
					return err
				}

				err = p.updateReclaimAllocationResultByPoolEntry(newPodEntries[podUID][containerName], poolEntry, nonReclaimActualBindingNUMAs)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("invalid qosLevel: %s for pod: %s/%s container: %s",
					allocationInfo.QoSLevel, allocationInfo.PodNamespace,
					allocationInfo.PodName, allocationInfo.ContainerName)
			}
		}
	}

	// The hard-partition invariant is enforced against the FINAL reclaim pool:
	// rampUpCPUs above is computed to include the reclaim pool (only the
	// ramp-up reclaim floor is subtracted), so plain ramp-up shared
	// allocations may end up overlapping the final reclaim pool after
	// reviseReclaimPool. Restore mutual exclusion here, before the pending
	// partition is committed. SNB ramp-up allocations are protected by the
	// bulkhead partition view and are not rematerialized here.
	if isRampUpReclaimHardPartitionEnabledWithConfig(dynamicConf) &&
		newPodEntries.HasActiveRampUp() {
		// The bulkhead BuildValidated view TPNR-trims the physical
		// ReclaimEffective to exclude active SNB (Shared-NUMA-Binding) ramp-up
		// CPUs (they stay a desired reclaim target). Write that physical value
		// back into the reclaim candidate before rematerializing ramp-up shared
		// business against it and before the precommit residual / mutual-exclusion
		// validators run; otherwise the candidate reclaim still carries the SNB
		// CPUs and the validators reject the commit as an overlap. This mirrors
		// the synchronous advisor-apply ordering on the same revision.
		//
		// This sync is scoped to active SNB ramp-up only: that is the single
		// quadrant whose TPNR trim the view computes but otherwise discards.
		// Dedicated/DNB ramp-up has no TPNR protection, and running the
		// view-driven reclaim rewrite there would drop the DNB ramp-up floor.
		if hasActiveSNBRampUp(newPodEntries) {
			commitOverride, err := p.buildAdjustmentCommitOverrideFromPodEntriesWithDynamicConfig(
				newPodEntries,
				allowSharedCoresOverlapReclaimedCores,
				disableDedicatedCoresOverlapReclaimedCores,
				dynamicConf,
			)
			if err != nil {
				return fmt.Errorf("build adjustment commit override for pool adjustment failed with error: %w", err)
			}
			if err := p.syncReclaimPoolWithAdjustmentCommitOverrideWithDynamicConfig(
				newPodEntries, commitOverride, dynamicConf, defaultSharePlan.eligibleCPUSet,
			); err != nil {
				return fmt.Errorf("sync reclaim pool with adjustment commit override for pool adjustment failed with error: %w", err)
			}
		}
		if err := p.rematerializeRampUpSharedAgainstFinalReclaim(newPodEntries); err != nil {
			return fmt.Errorf("rematerialize ramp-up shared allocations against final reclaim pool failed with error: %w", err)
		}
	}

	_, _, err = p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: stateRevision,
		entries:          newPodEntries,
		baseMachineState: machineState,
		allowOverlap:     allowSharedCoresOverlapReclaimedCores,
		disableDedicated: disableDedicatedCoresOverlapReclaimedCores,
		persist:          persistCheckpoint,
		source:           "pool adjustment",
		validate: func(entries state.PodEntries, ms state.NUMANodeMap, overlap, dedicated bool) error {
			// Terminal non-empty reclaim check after all mutations; shared helper
			// with preparePendingCPUPartition so the guard has one source of truth.
			if err := validateRequiredReclaimPool(entries); err != nil {
				return err
			}
			return p.validatePendingAdvisorPartitionViewWithRampUpExclusionWithDynamicConfig(entries, ms, overlap, dedicated, dynamicConf)
		},
		dynamicConfig:             dynamicConf,
		requireCoreAlignedReclaim: disableDedicatedCoresOverlapReclaimedCores,
		residualFloor:             rampUpReclaimFloor,
	})
	return err
}

// hasActiveSNBRampUp reports whether the pending entries contain a real
// container allocation that is both ramping up and Shared-NUMA-Binding. Only
// such allocations contribute to the bulkhead view's TransientProtectedNonReclaim
// (TPNR) set, so only they justify syncing the reclaim candidate to the view's
// TPNR-trimmed physical ReclaimEffective. Dedicated/DNB ramp-up has no TPNR
// protection and must keep the planned reclaim verbatim.
func hasActiveSNBRampUp(entries state.PodEntries) bool {
	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, ai := range containerEntries {
			if ai != nil && ai.RampUp && ai.CheckSharedNUMABinding() {
				return true
			}
		}
	}
	return false
}

func (p *DynamicPolicy) getSharedNUMABindingRampUpCPUSet(
	allocationInfo *state.AllocationInfo,
	machineState state.NUMANodeMap,
	unionDedicatedIsolatedCPUSet machine.CPUSet,
	notAllocatablePoolsCPUs machine.CPUSet,
	rampUpReclaimFloor machine.CPUSet,
	reclaimCandidate machine.CPUSet,
) (machine.CPUSet, map[int]machine.CPUSet, error) {
	if allocationInfo == nil {
		return machine.NewCPUSet(), nil, fmt.Errorf("nil allocationInfo")
	}
	numaHintStr := allocationInfo.Annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint]
	numaSet, err := machine.Parse(numaHintStr)
	if err != nil {
		return machine.NewCPUSet(), nil, fmt.Errorf("parse SNB numa hint %q failed: %w", numaHintStr, err)
	}
	if numaSet.Size() != 1 {
		return machine.NewCPUSet(), nil, fmt.Errorf("SNB ramp-up requires exactly one NUMA hint, got %s", numaSet.String())
	}

	numaID := numaSet.ToSliceNoSortInt()[0]
	numaState := machineState[numaID]
	if numaState == nil {
		return machine.NewCPUSet(), nil, fmt.Errorf("SNB ramp-up missing machine state for NUMA %d", numaID)
	}

	// The SNB ramp-up available domain is the hinted NUMA's free CPUs minus dedicated
	// isolated, forbidden/system pools, and the reclaim that must be reserved on this
	// NUMA. The reclaim reservation is the already-derived per-NUMA hard-partition
	// reclaim floor: it arrives both as the explicit rampUpReclaimFloor and as the
	// NUMA-local portion of the final reclaim candidate. Carving the NUMA-local reclaim
	// out here, before the exact AllocationResult is assigned, keeps the SNB disjoint
	// from the reclaim floor at selection time instead of relying on the TPNR view to
	// trim the physical reclaim afterwards (which would drop the floor below its target).
	//
	// This exclusion only applies to the hard-partition ramp-up shape: the per-NUMA
	// reclaim floor exists solely when EnableRampUpReclaimHardPartition is on and
	// shared/reclaim overlap is forbidden. Without the hard partition there is no floor
	// to protect and the SNB shares reclaim CPUs exactly as before, so the reclaim
	// candidate is left out of the available domain. Subtracting the node-wide reclaim
	// candidate is naturally scoped to the hinted NUMA because snbRampUpCPUs is already
	// restricted to that NUMA.
	snbRampUpCPUs := numaState.GetAvailableCPUSet(p.config.reservedCPUs).
		Difference(unionDedicatedIsolatedCPUSet).
		Difference(notAllocatablePoolsCPUs).
		Difference(rampUpReclaimFloor)
	reservedReclaimOnNUMA := machine.NewCPUSet()
	if p.isRampUpReclaimHardPartitionEnabled() && !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
		snbRampUpCPUs = snbRampUpCPUs.Difference(reclaimCandidate)
		reservedReclaimOnNUMA = reclaimCandidate.Intersection(numaState.GetAvailableCPUSet(p.config.reservedCPUs))
	}
	if snbRampUpCPUs.IsEmpty() {
		return machine.NewCPUSet(), nil, fmt.Errorf("SNB ramp-up CPUs are empty for NUMA %d after reserving reclaim floor %s",
			numaID, reservedReclaimOnNUMA.String())
	}

	assignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, snbRampUpCPUs)
	if err != nil {
		return machine.NewCPUSet(), nil, fmt.Errorf("calculate SNB ramp-up assignments for NUMA %d CPUs %s failed: %w",
			numaID, snbRampUpCPUs.String(), err)
	}
	if len(assignments) != 1 {
		return machine.NewCPUSet(), nil, fmt.Errorf("SNB ramp-up assignments crossed NUMA: %+v", assignments)
	}
	if _, ok := assignments[numaID]; !ok {
		return machine.NewCPUSet(), nil, fmt.Errorf("SNB ramp-up assignments %+v do not match hinted NUMA %d", assignments, numaID)
	}

	return snbRampUpCPUs, assignments, nil
}

func (p *DynamicPolicy) generateNUMABindingPoolsCPUSetInPlace(poolsCPUSet map[string]machine.CPUSet,
	poolsQuantityMap map[string]map[int]int, availableCPUs machine.CPUSet,
	dynamicConf *dynamicconfig.Configuration,
) (machine.CPUSet, error) {
	numaToPoolQuantityMap := make(map[int]map[string]int)
	originalAvailableCPUSet := availableCPUs.Clone()
	enableReclaim := dynamicConf != nil && dynamicConf.EnableReclaim

	for poolName, numaToQuantity := range poolsQuantityMap {
		for numaID, quantity := range numaToQuantity {
			if numaID == commonstate.FakedNUMAID {
				// only deal with numa_binding pools
				continue
			}

			if numaToPoolQuantityMap[numaID] == nil {
				numaToPoolQuantityMap[numaID] = make(map[string]int)
			}

			numaToPoolQuantityMap[numaID][poolName] = quantity
		}
	}

	for numaID, numaPoolsToQuantityMap := range numaToPoolQuantityMap {
		numaPoolsTotalQuantity := general.SumUpMapValues(numaPoolsToQuantityMap)
		numaCPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID).Difference(p.config.reservedCPUs)
		numaAvailableCPUs := numaCPUs.Intersection(availableCPUs)
		availableSize := numaAvailableCPUs.Size()

		general.Infof("numaID: %d, numaPoolsTotalQuantity: %d, availableSize: %d, enableReclaim: %v",
			numaID, numaPoolsTotalQuantity, availableSize, enableReclaim)

		var tErr error
		var leftCPUs machine.CPUSet
		if numaPoolsTotalQuantity <= availableSize && enableReclaim && !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
			leftCPUs, tErr = p.takeCPUsForPoolsInPlace(numaPoolsToQuantityMap, poolsCPUSet, numaAvailableCPUs)
			if tErr != nil {
				return originalAvailableCPUSet, fmt.Errorf("allocate cpus for numa_binding pools in NUMA: %d failed with error: %v",
					numaID, tErr)
			}
		} else {
			// numaPoolsTotalQuantity > availableSize || !enableReclaim || p.state.GetAllowSharedCoresOverlapReclaimedCores()
			// both allocate all numaAvailableCPUs proportionally
			leftCPUs, tErr = p.generateProportionalPoolsCPUSetInPlace(numaPoolsToQuantityMap, poolsCPUSet, numaAvailableCPUs)

			if tErr != nil {
				return originalAvailableCPUSet, fmt.Errorf("generateProportionalPoolsCPUSetInPlace for numa_binding pools in NUMA: %d failed with error: %v",
					numaID, tErr)
			}
		}

		availableCPUs = availableCPUs.Difference(numaCPUs).Union(leftCPUs)
	}

	return availableCPUs, nil
}

// generatePoolsAndIsolation is used to generate cpuset pools and isolated cpuset
// 1. allocate isolated cpuset for pod/containers, and divide total cores evenly if not possible to allocate
// 2. use the left cores to allocate among different pools
// 3. apportion to other pools if reclaimed is disabled
func (p *DynamicPolicy) selectDisjointReclaimFallback(
	availableCPUs machine.CPUSet,
	target int,
) (machine.CPUSet, error) {
	if p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return machine.NewCPUSet(), fmt.Errorf("select disjoint reclaim fallback requires cpu topology")
	}
	topology := p.machine.machineInfo.CPUTopology
	cpusPerCore := topology.CPUsPerCore()
	if cpusPerCore <= 0 {
		return machine.NewCPUSet(), fmt.Errorf("invalid cpus per core %d", cpusPerCore)
	}

	numaIDs := topology.CPUDetails.NUMANodes().ToSliceInt()
	capacityByNUMA := make(map[int]int, len(numaIDs))
	candidatesByNUMA := make(map[int]machine.CPUSet, len(numaIDs))
	for _, numaID := range numaIDs {
		candidates := availableCPUs.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		complete := takeCoreAlignedCPUSet(topology, candidates, candidates, candidates.Size())
		candidatesByNUMA[numaID] = complete
		capacityByNUMA[numaID] = complete.Size()
	}

	targets, err := machine.DistributeConfiguredHardReclaimFloor(
		capacityByNUMA, nil, target, cpusPerCore)
	if err != nil {
		return machine.NewCPUSet(), fmt.Errorf(
			"not enough eligible whole-core cpus for reclaim fallback: %w", err)
	}

	selected := machine.NewCPUSet()
	for _, numaID := range numaIDs {
		target := targets[numaID]
		if target == 0 {
			continue
		}
		preferred := p.reclaim.reservedReclaimedTopologyAwareAssignments[numaID]
		numaSelected := takeCoreAlignedCPUSet(
			topology, candidatesByNUMA[numaID], preferred, target)
		if numaSelected.Size() != target {
			return machine.NewCPUSet(), fmt.Errorf(
				"not enough eligible whole-core cpus for reclaim fallback in NUMA %d: got %d, want %d",
				numaID, numaSelected.Size(), target)
		}
		selected = selected.Union(numaSelected)
	}
	if selected.Size() != target {
		return machine.NewCPUSet(), fmt.Errorf(
			"not enough eligible whole-core cpus for reclaim fallback: got %d, want %d",
			selected.Size(), target)
	}
	return selected, nil
}

type generatePoolsAndIsolationOptions struct {
	rampUpReclaimFloor    machine.CPUSet
	enforceReclaimMinimum bool
	dynamicConf           *dynamicconfig.Configuration
	placement             *poolPlacementSnapshot
}

func (p *DynamicPolicy) generatePoolsAndIsolation(
	poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int, availableCPUs machine.CPUSet,
	reclaimOverlapShareRatio map[string]float64,
	options ...generatePoolsAndIsolationOptions) (poolsCPUSet map[string]machine.CPUSet,
	isolatedCPUSet map[string]map[string]machine.CPUSet, err error,
) {
	rampUpReclaimFloor := machine.NewCPUSet()
	enforceReclaimMinimum := true
	var dynamicConf *dynamicconfig.Configuration
	var placement *poolPlacementSnapshot
	if len(options) > 0 {
		generateOptions := options[0]
		rampUpReclaimFloor = generateOptions.rampUpReclaimFloor.Clone()
		enforceReclaimMinimum = generateOptions.enforceReclaimMinimum
		dynamicConf = generateOptions.dynamicConf
		placement = generateOptions.placement
	}
	poolsBindingNUMAs, nonBindingPoolsQuantityMap, explicitReclaimQuantity, parseErr := p.parsePoolAndIsolationQuantities(poolsQuantityMap, isolatedQuantityMap)
	if parseErr != nil {
		err = parseErr
		return
	}

	enableReclaim := dynamicConf != nil && dynamicConf.EnableReclaim
	allowOverlap := p.state.GetAllowSharedCoresOverlapReclaimedCores()
	disableDedicatedOverlap := p.state.GetDisableDedicatedCoresOverlapReclaimedCores()
	strictDisjointReclaim := enableReclaim && !allowOverlap && disableDedicatedOverlap
	configuredReclaimMinimum := machine.NewCPUSet()

	poolsCPUSet = make(map[string]machine.CPUSet)
	var nbpErr error
	availableCPUs, nbpErr = p.generateNUMABindingPoolsCPUSetInPlace(poolsCPUSet, poolsQuantityMap, availableCPUs, dynamicConf)
	if nbpErr != nil {
		err = fmt.Errorf("generateNUMABindingPoolsCPUSetInPlace failed with error: %v", nbpErr)
		return
	}

	// Reserve the configured reclaim minimum after hard NUMA-binding pools but
	// before soft non-binding pools and isolation entries. The active ramp-up
	// floor has already been removed by the caller, so this local set covers only
	// the remaining configured minimum. Reserving later lets soft pools consume
	// the capacity and may leave logical CPUs without a complete physical core.
	// Keep this separate from rampUpReclaimFloor: it is planning capacity, not an
	// active-domain floor, and must not change short-allocation/precommit scope.
	if strictDisjointReclaim && enforceReclaimMinimum && explicitReclaimQuantity == 0 {
		minimumOutsideFloor := p.reclaim.reservedReclaimedCPUsSize - rampUpReclaimFloor.Size()
		if minimumOutsideFloor > 0 {
			configuredReclaimMinimum, err = p.selectDisjointReclaimFallback(
				availableCPUs.Difference(rampUpReclaimFloor), minimumOutsideFloor)
			if err != nil {
				return
			}
			availableCPUs = availableCPUs.Difference(configuredReclaimMinimum)
		}
	}

	nonBindingAvailableCPUs := machine.NewCPUSet()
	for _, numaID := range p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		if poolsBindingNUMAs.Has(numaID) {
			continue
		}

		nonBindingAvailableCPUs = nonBindingAvailableCPUs.Union(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID).Intersection(availableCPUs))
	}
	availableCPUs = availableCPUs.Difference(nonBindingAvailableCPUs)

	nonBindingAvailableSize := nonBindingAvailableCPUs.Size()
	nonBindingPoolsTotalQuantity := general.SumUpMapValues(nonBindingPoolsQuantityMap)

	isolatedCPUSet = make(map[string]map[string]machine.CPUSet)
	isolatedTotalQuantity := general.SumUpMultipleMapValues(isolatedQuantityMap)

	general.Infof("isolatedTotalQuantity: %d, nonBindingPoolsTotalQuantity: %d, nonBindingAvailableSize: %d",
		isolatedTotalQuantity, nonBindingPoolsTotalQuantity, nonBindingAvailableSize)

	if placement == nil {
		placement = buildPoolPlacementSnapshot(
			p.state.GetPodEntries(), nil,
			poolsQuantityMap, isolatedQuantityMap, nonBindingAvailableCPUs, true)
	}
	preferredCPUsByPool := placement.borrowedByPool
	preferredCPUsByContainer := placement.dedicatedByContainer
	ownPoolPreferredCPUs := placement.ownByPool
	poolPreferredTiers := assemblePoolPreferredTiers(ownPoolPreferredCPUs, preferredCPUsByPool)
	var historicalReclaimDonors []machine.CPUSet
	if enableReclaim && !allowOverlap &&
		disableDedicatedOverlap && !placement.historicalReclaim.IsEmpty() {
		historicalReclaimDonors = append(historicalReclaimDonors, placement.historicalReclaim)
	}

	var tErr error
	if nonBindingPoolsTotalQuantity+isolatedTotalQuantity <= nonBindingAvailableSize {
		general.Infof("all pools and isolated containers could be allocated")

		isolatedCPUSet, nonBindingAvailableCPUs, tErr = p.takeCPUsForContainersWithPreferred(
			isolatedQuantityMap,
			nonBindingAvailableCPUs,
			preferredCPUsByContainer,
			historicalReclaimDonors...,
		)
		if tErr != nil {
			err = fmt.Errorf("allocate isolated cpus for dedicated_cores failed with error: %v", tErr)
			return
		}

		if !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
			nonBindingAvailableCPUs, tErr = p.takeCPUsForPoolsInPlaceWithTiers(
				nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs,
				poolPreferredTiers, ownPoolPreferredCPUs, historicalReclaimDonors...)
			if tErr != nil {
				err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
				return
			}
		} else {
			general.Infof("allowSharedCoresOverlapReclaimedCores is true, take all nonBindingAvailableCPUs for pools")
			nonBindingAvailableCPUs, tErr = p.generateProportionalPoolsCPUSetInPlaceWithTiers(
				nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs,
				poolPreferredTiers, ownPoolPreferredCPUs, historicalReclaimDonors...)

			if tErr != nil {
				err = fmt.Errorf("generateProportionalPoolsCPUSetInPlaceWithTiers pools failed with error: %v", tErr)
				return
			}
		}
	} else if nonBindingPoolsTotalQuantity <= nonBindingAvailableSize {
		general.Infof("all pools could be allocated, all isolated containers would be put to pools")

		if !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
			nonBindingAvailableCPUs, tErr = p.takeCPUsForPoolsInPlaceWithTiers(
				nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs,
				poolPreferredTiers, ownPoolPreferredCPUs, historicalReclaimDonors...)
			if tErr != nil {
				err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
				return
			}
		} else {
			general.Infof("allowSharedCoresOverlapReclaimedCores is true, take all nonBindingAvailableCPUs for pools")
			nonBindingAvailableCPUs, tErr = p.generateProportionalPoolsCPUSetInPlaceWithTiers(
				nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs,
				poolPreferredTiers, ownPoolPreferredCPUs, historicalReclaimDonors...)

			if tErr != nil {
				err = fmt.Errorf("generateProportionalPoolsCPUSetInPlaceWithTiers pools failed with error: %v", tErr)
				return
			}
		}
	} else if nonBindingPoolsTotalQuantity > 0 {
		general.Infof("can't allocate for all pools")

		nonBindingAvailableCPUs, tErr = p.generateProportionalPoolsCPUSetInPlaceWithTiers(
			nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs,
			poolPreferredTiers, ownPoolPreferredCPUs, historicalReclaimDonors...)

		if tErr != nil {
			err = fmt.Errorf("generateProportionalPoolsCPUSetInPlaceWithTiers pools failed with error: %v", tErr)
			return
		}
	}

	availableCPUs = availableCPUs.Union(nonBindingAvailableCPUs)

	// deal with reserve pool
	if poolsCPUSet[commonstate.PoolNameReserve].IsEmpty() {
		poolsCPUSet[commonstate.PoolNameReserve] = p.config.reservedCPUs.Clone()
		general.Infof("set pool %s:%s", commonstate.PoolNameReserve, poolsCPUSet[commonstate.PoolNameReserve].String())
	} else {
		err = fmt.Errorf("static pool %s result: %s is generated dynamically", commonstate.PoolNameReserve, poolsCPUSet[commonstate.PoolNameReserve].String())
		return
	}

	if !enableReclaim {
		// Reclaim disabled keeps the legacy downgrade behavior: use leftover CPUs as a
		// temporary reclaim bucket, then apportion them back to non-binding pools.
		poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(availableCPUs)
	}

	general.Infof("poolsCPUSet before reclaim apportion/overlap: %+v", poolsCPUSet)

	if !allowOverlap {
		if !enableReclaim && poolsCPUSet[commonstate.PoolNameReclaim].Size() > p.reclaim.reservedReclaimedCPUsSize {
			poolsCPUSet[commonstate.PoolNameReclaim] = p.apportionReclaimedPool(
				poolsCPUSet, poolsCPUSet[commonstate.PoolNameReclaim].Clone(), nonBindingPoolsQuantityMap)
			general.Infof("apportionReclaimedPool finished, current %s pool: %s",
				commonstate.PoolNameReclaim, poolsCPUSet[commonstate.PoolNameReclaim].String())
		}
	} else {
		// p.state.GetAllowSharedCoresOverlapReclaimedCores() == true
		poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(availableCPUs)
		if overlapErr := p.applyReclaimOverlapFromSharePools(poolsCPUSet, reclaimOverlapShareRatio); overlapErr != nil {
			err = overlapErr
			return
		}
	}

	general.Infof("poolsCPUSet after reclaim apportion/overlap: %+v", poolsCPUSet)

	currentPodEntries := p.state.GetPodEntries()
	if enableReclaim && !allowOverlap && poolsCPUSet[commonstate.PoolNameReclaim].IsEmpty() {
		if cset, cErr := currentPodEntries.GetCPUSetForPool(commonstate.PoolNameReclaim); cErr == nil && !cset.IsEmpty() {
			allocatedNonReclaimCPUs := machine.NewCPUSet()
			for poolName, poolCPUSet := range poolsCPUSet {
				if poolName == commonstate.PoolNameReclaim {
					continue
				}
				allocatedNonReclaimCPUs = allocatedNonReclaimCPUs.Union(poolCPUSet)
			}
			for _, containerCPUSetByName := range isolatedCPUSet {
				for _, containerCPUSet := range containerCPUSetByName {
					allocatedNonReclaimCPUs = allocatedNonReclaimCPUs.Union(containerCPUSet)
				}
			}
			remainingReclaim := cset.Difference(allocatedNonReclaimCPUs)
			if strictDisjointReclaim {
				remainingReclaim = remainingReclaim.Difference(rampUpReclaimFloor)
				remainingReclaim = takeCoreAlignedCPUSet(
					p.machine.machineInfo.CPUTopology,
					remainingReclaim,
					remainingReclaim,
					remainingReclaim.Size(),
				)
			}
			poolsCPUSet[commonstate.PoolNameReclaim] = remainingReclaim
			general.Infof("preserve current %s pool after deducting non-reclaim allocations, previous: %s, deducted: %s, current: %s",
				commonstate.PoolNameReclaim, cset.String(), allocatedNonReclaimCPUs.String(), poolsCPUSet[commonstate.PoolNameReclaim].String())
		}
	}
	if !configuredReclaimMinimum.IsEmpty() {
		poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(configuredReclaimMinimum)
	}

	if strictDisjointReclaim && enforceReclaimMinimum && explicitReclaimQuantity == 0 {
		minimumOutsideFloor := p.reclaim.reservedReclaimedCPUsSize - rampUpReclaimFloor.Size()
		if minimumOutsideFloor < 0 {
			minimumOutsideFloor = 0
		}
		currentReclaim := poolsCPUSet[commonstate.PoolNameReclaim]
		if currentReclaim.Size() < minimumOutsideFloor {
			err = fmt.Errorf("configured reclaim minimum planning invariant violated: got %d, want at least %d",
				currentReclaim.Size(), minimumOutsideFloor)
			return
		}
	} else if poolsCPUSet[commonstate.PoolNameReclaim].IsEmpty() {
		// Reclaim pool is empty after apportion: build the bounded fallback from the
		// full hard-eligible set (all CPUs minus reserved/forbidden/system/not-allocatable).
		// This is computed regardless of allowOverlap: legacy share-overlap may borrow CPUs
		// already held by shared pools, but hard-excluded CPUs (reserved, forbidden, system,
		// not-allocatable) are never borrowed. availableCPUs is a first guess; when it is
		// empty we rebuild from the full machine to get the overlap-eligible superset.
		priorReclaim := machine.NewCPUSet()
		if cset, cErr := currentPodEntries.GetCPUSetForPool(commonstate.PoolNameReclaim); cErr == nil {
			priorReclaim = cset
		}
		hardEligible := p.machine.machineInfo.CPUDetails.CPUs().
			Difference(p.config.reservedCPUs).
			Difference(p.getNotAllocatablePoolCPUs(currentPodEntries))
		// The fallback must not re-borrow ownership already apportioned in this very
		// generation, even when availableCPUs collapses to empty and we fall back to
		// hardEligible. Dedicated is excluded whenever disableDedicatedOverlap is set
		// (independent of strict/allowOverlap); share-type pools are excluded whenever
		// share overlap is not allowed, leaving the bounded legacy share-overlap
		// exception intact.
		hardEligible = hardEligible.Difference(modeProhibitedPoolCPUs(poolsCPUSet, reclaimFallbackMode{
			enableReclaim:           enableReclaim,
			allowSharedOverlap:      allowOverlap,
			disableDedicatedOverlap: disableDedicatedOverlap,
		}))
		// All paths: eligible must be a subset of hardEligible. availableCPUs is the
		// unallocated residual but may include forbidden/system CPUs already processed;
		// intersect it with hardEligible to guarantee hard exclusions on every path.
		eligible := availableCPUs.Intersection(hardEligible)
		if eligible.IsEmpty() {
			eligible = hardEligible
		}
		want := p.reclaim.reservedReclaimedCPUsSize
		mode := reclaimFallbackMode{
			enableReclaim:           enableReclaim,
			allowSharedOverlap:      allowOverlap,
			disableDedicatedOverlap: disableDedicatedOverlap,
		}
		if want <= 0 {
			// No explicit size requirement: use the bounded seed preference intersected
			// with hard-eligible. Never unfiltered-clone reservedReclaimedCPUSet.
			seed := p.reclaim.reservedReclaimedCPUSet.Clone().Intersection(eligible)
			general.Infof("legacy bounded reclaim seed in generatePoolsAndIsolation: %s", seed.String())
			poolsCPUSet[commonstate.PoolNameReclaim] = seed
		} else {
			reclaimFallback, fbErr := selectBoundedReclaimFallback(
				p.machine.machineInfo.CPUTopology,
				eligible,
				priorReclaim,
				p.reclaim.reservedReclaimedCPUSet,
				want,
				mode,
			)
			if fbErr != nil {
				// No unfiltered seed rescue here: if hard-eligible is empty and want>0,
				// the terminal final-validator rejects the empty reclaim pool (no-write).
				err = fbErr
				return
			}
			general.Infof("bounded reclaim fallback in generatePoolsAndIsolation: %s", reclaimFallback.String())
			poolsCPUSet[commonstate.PoolNameReclaim] = reclaimFallback
		}
	}

	// deal with forbidden pools and system exclusive pools
	p.appendForbiddenAndSystemPools(poolsCPUSet, currentPodEntries)

	return
}

// parsePoolAndIsolationQuantities normalizes the requested pool and isolation
// quantities: it collects binding/non-binding pool NUMAs, drops zero-quantity
// pools and isolation containers, and tallies the explicit reclaim quantity. It
// mutates its map arguments in place, matching the original inline normalization.
func (p *DynamicPolicy) parsePoolAndIsolationQuantities(
	poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int,
) (
	poolsBindingNUMAs sets.Int,
	nonBindingPoolsQuantityMap map[string]int,
	explicitReclaimQuantity int,
	err error,
) {
	poolsBindingNUMAs = sets.NewInt()
	poolsToSkip := make([]string, 0, len(poolsQuantityMap))
	nonBindingPoolsQuantityMap = make(map[string]int)
	explicitReclaimQuantity = 0
	for poolName, numaToQuantity := range poolsQuantityMap {
		if poolName == commonstate.PoolNameReclaim {
			for _, quantity := range numaToQuantity {
				if quantity > 0 {
					explicitReclaimQuantity += quantity
				}
			}
		}
		if len(numaToQuantity) > 1 {
			err = fmt.Errorf("pool: %s cross NUMAs: %+v", poolName, numaToQuantity)
			return
		} else if len(numaToQuantity) == 1 {
			for numaID, quantity := range numaToQuantity {
				if quantity == 0 {
					poolsToSkip = append(poolsToSkip, poolName)
				} else {
					if numaID != commonstate.FakedNUMAID {
						poolsBindingNUMAs.Insert(numaID)
					} else {
						nonBindingPoolsQuantityMap[poolName] = quantity
					}
				}
			}
		} else {
			poolsToSkip = append(poolsToSkip, poolName)
		}
	}

	for _, poolName := range poolsToSkip {
		general.Warningf("pool: %s with 0 quantity, skip generate", poolName)
		delete(poolsQuantityMap, poolName)
	}

	// clear isolated map with zero quantity
	for podUID, containerEntries := range isolatedQuantityMap {
		for containerName, quantity := range containerEntries {
			if quantity == 0 {
				general.Warningf("isolated pod: %s, container: %s with 0 quantity, skip generate it", podUID, containerName)
				delete(containerEntries, containerName)
			}
		}
		if len(containerEntries) == 0 {
			general.Warningf(" isolated pod: %s all container entries skipped", podUID)
			delete(isolatedQuantityMap, podUID)
		}
	}
	return
}

// applyReclaimOverlapFromSharePools reverse-takes CPUs from each share-type pool
// that carries an overlap ratio and merges them into the reclaim pool.
func (p *DynamicPolicy) applyReclaimOverlapFromSharePools(
	poolsCPUSet map[string]machine.CPUSet,
	reclaimOverlapShareRatio map[string]float64,
) error {
	for poolName, cset := range poolsCPUSet {
		if ratio, found := reclaimOverlapShareRatio[poolName]; found && ratio > 0 {

			req := int(math.Ceil(float64(cset.Size()) * ratio))

			// if p.state.GetAllowSharedCoresOverlapReclaimedCores() == false, we will take cpus for reclaim pool lastly,
			// else we also should take cpus for reclaim pool reversely overlapping with share type pool to aviod cpuset jumping obviously
			var tErr error
			overlapCPUs, _, tErr := calculator.TakeByNUMABalanceReversely(p.machine.machineInfo, cset, req)
			if tErr != nil {
				return fmt.Errorf("take overlapCPUs from: %s to %s by ratio: %.4f failed with err: %v",
					poolName, commonstate.PoolNameReclaim, ratio, tErr)
			}

			general.Infof("merge overlapCPUs: %s from pool: %s to %s by ratio: %.4f",
				overlapCPUs.String(), poolName, commonstate.PoolNameReclaim, ratio)
			poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(overlapCPUs)
		}
	}
	return nil
}

// appendForbiddenAndSystemPools copies the forbidden pools and the system
// exclusive pools from the current pod entries into the generated pool set.
func (p *DynamicPolicy) appendForbiddenAndSystemPools(
	poolsCPUSet map[string]machine.CPUSet,
	currentPodEntries state.PodEntries,
) {
	for _, poolName := range state.ForbiddenPools.List() {
		cset, err := currentPodEntries.GetCPUSetForPool(poolName)
		if err != nil {
			general.Infof("can't get CPUSet for pool %s: %v", poolName, err)
			continue
		}
		poolsCPUSet[poolName] = cset.Clone()
	}

	// add system exclusive pools, so that system cores pod with pool set to one of them could be allocated
	for poolName, entry := range currentPodEntries {
		if !commonstate.IsSystemPool(poolName) {
			continue
		}
		allocationInfo := entry.GetPoolEntry()
		if allocationInfo == nil {
			continue
		}
		poolsCPUSet[poolName] = allocationInfo.AllocationResult.Clone()
	}
}

func (p *DynamicPolicy) generateProportionalPoolsCPUSetInPlace(poolsQuantityMap map[string]int,
	poolsCPUSet map[string]machine.CPUSet, availableCPUs machine.CPUSet,
) (machine.CPUSet, error) {
	availableSize := availableCPUs.Size()

	proportionalPoolsQuantityMap, totalProportionalPoolsQuantity := getProportionalPoolsQuantityMap(poolsQuantityMap, availableSize)

	general.Infof("poolsQuantityMap: %v, proportionalPoolsQuantityMap: %v", poolsQuantityMap, proportionalPoolsQuantityMap)

	// availableSize can't satisfy every pool has at least one cpu,
	// we make all pools equals to availableCPUs in this case.
	if totalProportionalPoolsQuantity > availableSize {
		for poolName := range poolsQuantityMap {
			if _, found := poolsCPUSet[poolName]; found {
				return availableCPUs.Clone(), fmt.Errorf("duplicated pool: %s", poolName)
			}

			poolsCPUSet[poolName] = availableCPUs.Clone()
		}

		return machine.NewCPUSet(), nil
	} else {
		var err error
		availableCPUs, err = p.takeCPUsForPoolsInPlace(proportionalPoolsQuantityMap, poolsCPUSet, availableCPUs)
		if err != nil {
			return availableCPUs, err
		}
	}

	return availableCPUs, nil
}

func getProportionalPoolsQuantityMap(originalPoolsQuantityMap map[string]int, availableSize int) (map[string]int, int) {
	totalProportionalPoolsQuantity := 0
	originalPoolsTotalQuantity := general.SumUpMapValues(originalPoolsQuantityMap)
	proportionalPoolsQuantityMap := make(map[string]int)

	for poolName, poolQuantity := range originalPoolsQuantityMap {
		proportionalSize := general.Max(getProportionalSize(poolQuantity, originalPoolsTotalQuantity, availableSize, true /*ceil*/), 1)
		proportionalPoolsQuantityMap[poolName] = proportionalSize
		totalProportionalPoolsQuantity += proportionalSize
	}

	poolNames := make([]string, 0, len(proportionalPoolsQuantityMap))

	for poolName := range proportionalPoolsQuantityMap {
		poolNames = append(poolNames, poolName)
	}

	sort.Slice(poolNames, func(x, y int) bool {
		// sort in descending order
		return proportionalPoolsQuantityMap[poolNames[x]] > proportionalPoolsQuantityMap[poolNames[y]]
	})

	// corner case: after divide, the total count goes to be bigger than available total
	for totalProportionalPoolsQuantity > availableSize {
		curTotalProportionalPoolsQuantity := totalProportionalPoolsQuantity

		for _, poolName := range poolNames {
			quantity := proportionalPoolsQuantityMap[poolName]

			if quantity > 1 && totalProportionalPoolsQuantity > 0 {
				quantity--
				totalProportionalPoolsQuantity--
				proportionalPoolsQuantityMap[poolName] = quantity

				if totalProportionalPoolsQuantity == availableSize {
					break
				}
			}
		}

		// availableSize can't satisfy every pool has at least one cpu
		if curTotalProportionalPoolsQuantity == totalProportionalPoolsQuantity {
			break
		}
	}

	return proportionalPoolsQuantityMap, totalProportionalPoolsQuantity
}

// apportionReclaimedPool tries to allocate reclaimed cores to none-binding && none-reclaimed pools.
// if we disable reclaim on current node, this could be used a down-grade strategy
// to disable reclaimed workloads in emergency
func (p *DynamicPolicy) apportionReclaimedPool(poolsCPUSet map[string]machine.CPUSet, reclaimedCPUs machine.CPUSet, nonBindingPoolsQuantityMap map[string]int) machine.CPUSet {
	totalSize := 0
	for poolName, poolCPUs := range poolsCPUSet {
		if state.ResidentPools.Has(poolName) {
			continue
		} else if _, found := nonBindingPoolsQuantityMap[poolName]; !found {
			// numa-binding && none-reclaimed pools already handled in generateNUMABindingPoolsCPUSetInPlace
			continue
		}
		totalSize += poolCPUs.Size()
	}

	availableSize := reclaimedCPUs.Size() - p.reclaim.reservedReclaimedCPUsSize
	if availableSize <= 0 || totalSize == 0 {
		return reclaimedCPUs
	}

	// Iterate the eligible pools in a stable, sorted order. The loop below
	// consumes CPUs out of the shared reclaimedCPUs set pool-by-pool, so the
	// visit order decides which pool owns which physical CPUs. Ranging the map
	// directly would make the assignment non-deterministic across runs.
	eligiblePoolNames := make([]string, 0, len(poolsCPUSet))
	for poolName := range poolsCPUSet {
		if state.ResidentPools.Has(poolName) {
			continue
		} else if _, found := nonBindingPoolsQuantityMap[poolName]; !found {
			// numa-binding && none-reclaimed pools already handled in generateNUMABindingPoolsCPUSetInPlace
			continue
		}
		eligiblePoolNames = append(eligiblePoolNames, poolName)
	}
	sort.Strings(eligiblePoolNames)

	for _, poolName := range eligiblePoolNames {
		poolCPUs := poolsCPUSet[poolName]

		proportionalSize := general.Max(getProportionalSize(poolCPUs.Size(), totalSize, availableSize, false /*ceil*/), 1)

		// lend whole physical cores only, so the reclaim residual (and the
		// borrowing primary pool) never keeps a lone SMT sibling of a reclaim
		// core. crop the proportional demand DOWN to a whole-core multiple: a
		// sub-core demand lends nothing and keeps the intact core in reclaim,
		// preserving primary/reclaim physical-core isolation.
		coreAlignedSize := proportionalSize
		if cpusPerCore := p.machine.machineInfo.CPUTopology.CPUsPerCore(); cpusPerCore > 1 {
			coreAlignedSize = (proportionalSize / cpusPerCore) * cpusPerCore
		}
		if coreAlignedSize <= 0 {
			continue
		}

		var err error
		var cpuset machine.CPUSet
		// TakeByNUMABalance grabs complete cores balanced across NUMAs (it only
		// falls back to per-cpu when the demand is not a whole-core multiple,
		// which the crop above rules out on a core-aligned reclaim input).
		cpuset, reclaimedCPUs, err = calculator.TakeByNUMABalance(p.machine.machineInfo, reclaimedCPUs, coreAlignedSize)
		if err != nil {
			general.Errorf("take %d cpus from reclaimedCPUs: %s, size: %d failed with error: %v",
				coreAlignedSize, reclaimedCPUs.String(), reclaimedCPUs.Size(), err)
			return reclaimedCPUs
		}

		poolsCPUSet[poolName] = poolCPUs.Union(cpuset)
		general.Infof("take %s to %s; prev: %s, current: %s", cpuset.String(), poolName, poolCPUs.String(), poolsCPUSet[poolName].String())

		if reclaimedCPUs.Size() <= p.reclaim.reservedReclaimedCPUsSize {
			break
		}
	}

	// fail-loud net: the reclaim residual must never hold a partial physical
	// core. a violation signals a broken upstream invariant (non-core-aligned
	// reclaim input) that must be surfaced, not masked.
	if err := assertCoreAligned(reclaimedCPUs, p.machine.machineInfo.CPUTopology); err != nil {
		general.Errorf("apportionReclaimedPool reclaim residual not core-aligned: %v", err)
	}

	return reclaimedCPUs
}

func (p *DynamicPolicy) takeCPUsForPoolsInPlace(poolsQuantityMap map[string]int,
	poolsCPUSet map[string]machine.CPUSet,
	availableCPUs machine.CPUSet,
) (machine.CPUSet, error) {
	originalAvailableCPUSet := availableCPUs.Clone()
	var poolsCPUSetToAdd map[string]machine.CPUSet
	var tErr error
	poolsCPUSetToAdd, availableCPUs, tErr = p.takeCPUsForPools(poolsQuantityMap, availableCPUs)
	if tErr != nil {
		return originalAvailableCPUSet, fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
	}

	for poolName, cset := range poolsCPUSetToAdd {
		if _, found := poolsCPUSet[poolName]; found {
			return originalAvailableCPUSet, fmt.Errorf("duplicated pool: %s", poolName)
		}

		poolsCPUSet[poolName] = cset
	}

	return availableCPUs, nil
}

// takeCPUsForPools tries to allocate cpuset for each given pool,
// and it will consider the total available cpuset during calculation.
// the returned value includes cpuset pool map and remaining available cpuset.
func (p *DynamicPolicy) takeCPUsForPools(poolsQuantityMap map[string]int,
	availableCPUs machine.CPUSet,
) (map[string]machine.CPUSet, machine.CPUSet, error) {
	poolsCPUSet := make(map[string]machine.CPUSet)
	clonedAvailableCPUs := availableCPUs.Clone()

	// to avoid random map iteration sequence to generate pools randomly
	sortedPoolNames := general.GetSortedMapKeys(poolsQuantityMap)
	for _, poolName := range sortedPoolNames {
		req := poolsQuantityMap[poolName]
		general.Infof("allocated for pool: %s with req: %d", poolName, req)

		var err error
		var cset machine.CPUSet
		cset, availableCPUs, err = calculator.TakeByNUMABalance(p.machine.machineInfo, availableCPUs, req)
		if err != nil {
			return nil, clonedAvailableCPUs, fmt.Errorf("take cpu for pool: %s of req: %d failed with error: %v",
				poolName, req, err)
		}
		poolsCPUSet[poolName] = cset
	}
	return poolsCPUSet, availableCPUs, nil
}

func (p *DynamicPolicy) shouldSharedCoresRampUp(ctx context.Context, podUID string) bool {
	if p.isSharedCoresRampUpDisabled() {
		general.Infof("shared cores ramp up is disabled by dynamic config, podUID: %s", podUID)
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	pod, err := p.meta.metaServer.GetPod(ctx, podUID)

	if err != nil {
		general.Errorf("get pod: %s failed with error: %v during admission, try to ramp up it", podUID, err)
		return true
	} else if pod == nil {
		general.Infof("can't get pod: %s from metaServer, not try to ramp up it", podUID)
		return false
	} else if !native.PodIsPending(pod) {
		general.Infof("pod: %s/%s isn't pending(not admit firstly), not try to ramp up it", pod.Namespace, pod.Name)
		return false
	} else {
		general.Infof("pod: %s/%s isn't active, try to ramp up it", pod.Namespace, pod.Name)
		return true
	}
}

func (p *DynamicPolicy) isSharedCoresRampUpDisabled() bool {
	if p.config.dynamicConfig == nil {
		return false
	}
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	return dyn != nil && dyn.DisableSharedCoresRampUp
}

func (p *DynamicPolicy) doAndCheckPutAllocationInfoPodResizingAware(
	ctx context.Context,
	originAllocationInfo, allocationInfo *state.AllocationInfo,
	incrByReq, podInplaceUpdateResizing, persistCheckpoint bool,
) (*state.AllocationInfo, error) {
	if allocationInfo == nil {
		return nil, fmt.Errorf("doAndCheckPutAllocationInfo got nil allocationInfo")
	}

	// need to adjust pools and putAllocationsAndAdjustAllocationEntries will set the allocationInfo after adjusted
	err := p.putAllocationsAndAdjustAllocationEntriesResizeAware(
		ctx, []*state.AllocationInfo{originAllocationInfo}, []*state.AllocationInfo{allocationInfo},
		incrByReq, podInplaceUpdateResizing, persistCheckpoint)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s putAllocationsAndAdjustAllocationEntriesResizeAware failed with error: %v",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
		return nil, fmt.Errorf("putallocationsandadjustallocationentries failed with error: %s", strings.ToLower(err.Error()))
	}

	checkedAllocationInfo := p.state.GetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName)
	if checkedAllocationInfo == nil {
		general.Errorf("pod: %s/%s, container: %s get nil allocationInfo after putAllocationsAndAdjustAllocationEntries",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
		return nil, fmt.Errorf("allocationInfo missing after putAllocationsAndAdjustAllocationEntries: pod=%s/%s uid=%s container=%s ownerPool=%s specifiedPool=%s stateRevision=%d",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.PodUid, allocationInfo.ContainerName,
			allocationInfo.OwnerPoolName, allocationInfo.GetSpecifiedPoolName(), p.state.GetRevision())
	}

	return checkedAllocationInfo, nil
}

func (p *DynamicPolicy) doAndCheckPutAllocationInfo(
	ctx context.Context,
	allocationInfo *state.AllocationInfo,
	incrByReq, persistCheckpoint bool,
) (*state.AllocationInfo, error) {
	return p.doAndCheckPutAllocationInfoPodResizingAware(
		ctx, nil, allocationInfo, incrByReq, false, persistCheckpoint)
}

func (p *DynamicPolicy) getReclaimOverlapShareRatio(entries state.PodEntries) (map[string]float64, error) {
	if !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
		return nil, nil
	}

	reclaimPool, ok := entries[commonstate.PoolNameReclaim]
	if !ok {
		return nil, fmt.Errorf("reclaim pool misses in current entries")
	}
	fakeEntry, ok := reclaimPool[commonstate.FakedContainerName]
	if !ok || fakeEntry.AllocationResult.IsEmpty() {
		return nil, fmt.Errorf("reclaim pool misses in current entries")
	}

	reclaimOverlapShareRatio := make(map[string]float64)

	curReclaimCPUSet := fakeEntry.AllocationResult

	// Iterate through all pools to calculate overlap ratios
	for poolName, subEntries := range entries {
		if !subEntries.IsPoolEntry() {
			continue
		}

		allocationInfo := subEntries.GetPoolEntry()

		if allocationInfo != nil && commonstate.GetPoolType(poolName) == commonstate.PoolNameShare {
			if allocationInfo.AllocationResult.IsEmpty() {
				continue
			}

			shareTypePoolSize := allocationInfo.AllocationResult.Size()
			overlapSize := allocationInfo.AllocationResult.Intersection(curReclaimCPUSet).Size()

			if overlapSize == 0 {
				continue
			}

			reclaimOverlapShareRatio[poolName] = float64(overlapSize) / float64(shareTypePoolSize)
		}
	}

	// If no overlap was found, calculate non-overlap ratios
	if len(reclaimOverlapShareRatio) == 0 {
		reclaimNonOverlapShareRatio := make(map[string]float64)

		// Iterate over all sub-entries to compute non-overlap ratios
		for _, subEntries := range entries {
			if subEntries.IsPoolEntry() {
				continue
			}

			for _, allocationInfo := range subEntries {
				if allocationInfo == nil || allocationInfo.AllocationResult.IsEmpty() {
					continue
				}

				// Only process shared pools
				poolName := allocationInfo.GetPoolName()
				if commonstate.GetPoolType(poolName) == commonstate.PoolNameShare {
					requestQuantity := allocationInfo.RequestQuantity
					if requestQuantity > 0 {
						reclaimNonOverlapShareRatio[poolName] += requestQuantity / float64(allocationInfo.AllocationResult.Size())
					}
				}
			}
		}

		// Convert non-overlap ratios to overlap ratios
		for poolName, ratio := range reclaimNonOverlapShareRatio {
			reclaimOverlapShareRatio[poolName] = 1.0 - ratio
		}
	}
	return reclaimOverlapShareRatio, nil
}

func (p *DynamicPolicy) systemCoresHintHandler(_ context.Context, request *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	return util.PackResourceHintsResponse(request, string(v1.ResourceCPU),
		map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceCPU): nil, // indicates that there is no numa preference
		})
}

func (p *DynamicPolicy) systemCoresAllocationHandler(ctx context.Context, req *pluginapi.ResourceRequest, persistCheckpoint bool) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.allocationSidecarHandler(ctx, req, apiconsts.PodAnnotationQoSLevelSystemCores, persistCheckpoint)
	}

	// Read the live bypass switch once for this whole operation.
	live := p.shouldBypassCPUSetAdjustment()
	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSystemCores),
		InitTimestamp: time.Now().Format(util.QRMTimeFormat),
	}
	// Only a brand-new MAIN system container gets an initial omission decision.
	// An existing system container re-admitted here inherits its persisted value
	// so a recorded "false" cannot be upgraded back to "true".
	if origin := p.state.GetAllocationInfo(req.PodUid, req.ContainerName); origin == nil {
		initNewMainContainerCPUSetOmission(allocationInfo, req.Annotations, live)
	} else {
		inheritCPUSetOmission(allocationInfo, origin)
		if !cpusetOmissionAllowed(allocationInfo, live) {
			markCPUSetExposed(allocationInfo)
		}
	}
	poolCPUSet, topologyAwareAssignments, err := p.getSystemPoolCPUSetAndNumaAwareAssignments(p.state.GetPodEntries(), allocationInfo)
	if err != nil {
		general.ErrorS(err, "unable to get system pool cpuset and topologyAwareAssignments",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		return nil, err
	}

	systemPoolName, err := allocationInfo.GetSpecifiedSystemPoolName()
	if err != nil {
		return nil, err
	}

	general.InfoS("allocate system pool cpuset successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"poolName", systemPoolName,
		"result", poolCPUSet.String(),
		"topologyAwareAssignments", topologyAwareAssignments)

	allocationInfo.OwnerPoolName = systemPoolName
	allocationInfo.AllocationResult = poolCPUSet
	allocationInfo.OriginalAllocationResult = poolCPUSet.Clone()
	allocationInfo.TopologyAwareAssignments = topologyAwareAssignments
	allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)

	if err := p.updateAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, nil, allocationInfo, persistCheckpoint); err != nil {
		return nil, err
	}
	podEntries := p.state.GetPodEntries()

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s generateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("generateMachineStateFromPodEntries failed with error: %v", err)
	}
	if err := p.state.SetMachineState(updatedMachineState, persistCheckpoint); err != nil {
		return nil, fmt.Errorf("set system allocation machine state: %w", err)
	}

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req, allocationInfo.Annotations)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	if cpusetOmissionAllowed(allocationInfo, live) {
		clearCPUSetInAllocation(resp.AllocationResult)
	}
	return resp, nil
}

// getSystemPoolCPUSetAndNumaAwareAssignments gets the system pool cpuset and topologyAwareAssignments for the allocationInfo.
// For system shared pools, use the specified pool name, and these pools must exist in the podEntries.
// For system exclusive pool, use the system pool name, and also these pools must exist in the podEntries.
func (p *DynamicPolicy) getSystemPoolCPUSetAndNumaAwareAssignments(podEntries state.PodEntries,
	allocationInfo *state.AllocationInfo,
) (machine.CPUSet, map[int]machine.CPUSet, error) {
	if allocationInfo == nil {
		return machine.CPUSet{}, nil, fmt.Errorf("allocationInfo is nil")
	}

	poolCPUSet := machine.NewCPUSet()
	specifiedPoolName := allocationInfo.GetSpecifiedPoolName()
	if specifiedPoolName != commonstate.EmptyOwnerPoolName {
		poolName := specifiedPoolName
		if _, ok := p.config.dynamicConfig.GetDynamicConfiguration().SystemExclusivePool[specifiedPoolName]; ok {
			poolName = commonstate.GetSystemPoolName(specifiedPoolName)
		}

		for pool, entries := range podEntries {
			if !entries.IsPoolEntry() {
				continue
			}

			if pool == poolName || strings.HasPrefix(pool, poolName) {
				poolCPUSet = poolCPUSet.Union(entries.GetPoolEntry().AllocationResult)
				general.Infof("pod: %s/%s, container: %s get system pool cpuset from pool: %s, cpuset: %s", allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName, pool, entries.GetPoolEntry().AllocationResult.String())
			}
		}
	}

	// if pool set is empty, try to get default cpuset
	if poolCPUSet.IsEmpty() {
		// if the pod is numa binding, get the default cpuset from machine state
		if allocationInfo.CheckNUMABinding() {
			poolCPUSet = p.state.GetMachineState().GetAvailableCPUSet(p.config.reservedCPUs)
		}

		// if the default cpuset is empty or no numa binding, use all cpuset as default cpuset
		if poolCPUSet.IsEmpty() {
			poolCPUSet = p.machine.machineInfo.CPUDetails.CPUs()
		}
		general.Infof("pod: %s/%s, container: %s get system pool cpuset from default cpuset: %s", allocationInfo.PodNamespace, allocationInfo.PodName,
			allocationInfo.ContainerName, poolCPUSet.String())
	}

	if poolCPUSet.IsEmpty() {
		return machine.CPUSet{}, nil, fmt.Errorf("no system pool cpuset for pool %s", specifiedPoolName)
	}

	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, poolCPUSet)
	if err != nil {
		return machine.CPUSet{}, nil, fmt.Errorf("unable to get numa aware assignments: %v", err)
	}

	return poolCPUSet, topologyAwareAssignments, nil
}

func (p *DynamicPolicy) getAllocationPoolEntry(allocationInfo *state.AllocationInfo, ownerPoolName string, entries state.PodEntries) (*state.AllocationInfo, error) {
	poolEntry := entries[ownerPoolName][commonstate.FakedContainerName]
	if poolEntry != nil {
		return poolEntry, nil
	}

	errMsg := fmt.Sprintf("cpu advisor doesn't return entry for pool: %s and it's referred by pod: %s/%s, container: %s, qosLevel: %s",
		ownerPoolName, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)

	general.Errorf("%s", errMsg)

	_ = p.emitter.emitter.StoreInt64(util.MetricNameOrphanContainer, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "podNamespace", Val: allocationInfo.PodNamespace},
		metrics.MetricTag{Key: "podName", Val: allocationInfo.PodName},
		metrics.MetricTag{Key: "containerName", Val: allocationInfo.ContainerName},
		metrics.MetricTag{Key: "poolName", Val: ownerPoolName})
	return nil, fmt.Errorf(errMsg)
}

func (p *DynamicPolicy) updateReclaimAllocationResultByPoolEntry(allocationInfo *state.AllocationInfo,
	poolEntry *state.AllocationInfo, nonReclaimActualBindingNUMAs machine.CPUSet,
) error {
	numaID, err := allocationInfo.GetSpecifiedNUMABindingNUMAID()
	if err != nil {
		return err
	}

	getActualNUMABindingResult := func(topologyAwareAssignments map[int]machine.CPUSet) (machine.CPUSet, map[int]machine.CPUSet, error) {
		var (
			actualTopologyAwareAssignments map[int]machine.CPUSet
			actualAllocationResult         machine.CPUSet
		)
		if numaID != commonstate.FakedNUMAID {
			// Share the single ownership meaning with the validator: an active RNB
			// member needs a ready (present, initialized, non-empty) owner-pool
			// bucket on its target NUMA. A missing, uninitialized, or explicit-empty
			// bucket fails closed here, not just at validation time.
			rb := lookupReclaimBucketIn(topologyAwareAssignments, numaID)
			if rb.class != reclaimBucketReady {
				return machine.CPUSet{}, nil, rb.failClosedError(poolEntry, numaID)
			}
			cpuSet := rb.cpus
			actualAllocationResult = cpuSet.Clone()
			actualTopologyAwareAssignments = map[int]machine.CPUSet{
				numaID: cpuSet.Clone(),
			}
		} else {
			// set non-RNB numa allocation info
			numaSet := machine.NewCPUSet()
			newTopologyAwareAssignments := make(map[int]machine.CPUSet)
			for numaNode, cpuSet := range topologyAwareAssignments {
				if !nonReclaimActualBindingNUMAs.Contains(numaNode) {
					continue
				}

				if cpuSet.Size() > 0 {
					numaSet = numaSet.Union(cpuSet)
				}

				newTopologyAwareAssignments[numaNode] = cpuSet.Clone()
			}
			actualAllocationResult = numaSet
			actualTopologyAwareAssignments = newTopologyAwareAssignments
		}
		return actualAllocationResult, actualTopologyAwareAssignments, nil
	}

	actualAllocationResult, actualTopologyAwareAssignments, err := getActualNUMABindingResult(machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments))
	if err != nil {
		return fmt.Errorf("get actual NUMA binding result: %w", err)
	}

	actualOriginalAllocationResult, actualOriginalTopologyAwareAssignments, err := getActualNUMABindingResult(machine.DeepcopyCPUAssignment(poolEntry.OriginalTopologyAwareAssignments))
	if err != nil {
		return fmt.Errorf("get original actual NUMA binding result: %w", err)
	}

	general.Infof("put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
		allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, poolEntry.OwnerPoolName, allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

	allocationInfo.OwnerPoolName = poolEntry.OwnerPoolName
	allocationInfo.AllocationResult = actualAllocationResult
	allocationInfo.OriginalAllocationResult = actualOriginalAllocationResult
	allocationInfo.TopologyAwareAssignments = actualTopologyAwareAssignments
	allocationInfo.OriginalTopologyAwareAssignments = actualOriginalTopologyAwareAssignments
	return nil
}

// isRampUpReclaimHardPartitionEnabled reports whether the ramp-up reclaim hard
// partition feature is enabled by the current dynamic configuration.
func (p *DynamicPolicy) isRampUpReclaimHardPartitionEnabled() bool {
	var dyn *dynamicconfig.Configuration
	if p != nil && p.config.dynamicConfig != nil {
		dyn = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	return isRampUpReclaimHardPartitionEnabledWithConfig(dyn)
}

func isRampUpReclaimHardPartitionEnabledWithConfig(dyn *dynamicconfig.Configuration) bool {
	return dyn != nil && dyn.EnableReclaim && dyn.EnableRampUpReclaimHardPartition
}

func (p *DynamicPolicy) getSharedNUMABindingCPUIncrRatio() float64 {
	var dyn *dynamicconfig.Configuration
	if p != nil && p.config.dynamicConfig != nil {
		dyn = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	return getSharedNUMABindingCPUIncrRatioWithConfig(dyn)
}

func getSharedNUMABindingCPUIncrRatioWithConfig(dyn *dynamicconfig.Configuration) float64 {
	if isRampUpReclaimHardPartitionEnabledWithConfig(dyn) {
		return cpuconsts.CPUIncrRatioDefault
	}
	return cpuconsts.CPUIncrRatioSharedCoresNUMABinding
}

// isReclaimEnabled reports the node-level reclaim switch from dynamic config.
func (p *DynamicPolicy) isReclaimEnabled() bool {
	if p.config.dynamicConfig == nil {
		return false
	}
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	return dyn != nil && dyn.EnableReclaim
}

// deriveRampUpReclaimFloor selects immutable per-NUMA targets only after
// dedicated/reclaim disjoint mode has been negotiated. Legacy overlap keeps
// the historical available-capacity-based target calculation.
//
// rampUpDomains carries the reclaim domains that are entering ramp-up with the
// current request; it is unioned with the domains already recorded in
// candidateEntries. A domain is FakedNUMAID for the global (non-binding)
// domain, or a real NUMA id for a NUMA-binding ramp-up. The hard reclaim floor
// is only imposed on the NUMAs those domains actually cover, so a NUMA with no
// local ramp-up (e.g. a NUMA-exclusive region) is no longer pressed to the
// reserved floor.
func (p *DynamicPolicy) deriveRampUpReclaimFloor(
	machineState state.NUMANodeMap,
	candidateEntries state.PodEntries,
	rampUpDomains sets.Int,
) (machine.CPUSet, error) {
	return p.deriveRampUpReclaimFloorForMode(
		machineState,
		candidateEntries,
		rampUpDomains,
		p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
	)
}

func (p *DynamicPolicy) deriveRampUpReclaimFloorForMode(
	machineState state.NUMANodeMap,
	candidateEntries state.PodEntries,
	rampUpDomains sets.Int,
	immutablePerNUMA bool,
) (machine.CPUSet, error) {
	return p.deriveRampUpReclaimFloorForModeWithDynamicConfig(
		machineState, candidateEntries, rampUpDomains, immutablePerNUMA,
		p.config.dynamicConfig.GetDynamicConfiguration())
}

// deriveRampUpReclaimFloorForModeWithDynamicConfig derives the reclaim floor
// from the immutable configuration snapshot owned by the current advisor
// attempt. Keeping both the scalar floor and dynamic NUMA policy in that
// canonical snapshot prevents a single plan from mixing config generations.
func (p *DynamicPolicy) deriveRampUpReclaimFloorForModeWithDynamicConfig(
	machineState state.NUMANodeMap,
	candidateEntries state.PodEntries,
	rampUpDomains sets.Int,
	immutablePerNUMA bool,
	dynamicConf *dynamicconfig.Configuration,
) (machine.CPUSet, error) {
	floor := machine.NewCPUSet()
	if !isRampUpReclaimHardPartitionEnabledWithConfig(dynamicConf) ||
		p.machine.machineInfo == nil {
		return floor, nil
	}

	// The effective ramp-up domains are the union of the ramp-up domains
	// already recorded in the candidate entries and the domains entering with
	// the current request. A missing/ambiguous domain derivation fails closed
	// (the error propagates) rather than widening the floor node-wide.
	effectiveRampUpDomains, err := candidateEntries.ActiveRampUpDomains(p.machine.machineInfo.CPUTopology)
	if err != nil {
		return machine.NewCPUSet(), fmt.Errorf("derive ramp-up reclaim floor: resolve active ramp-up domains: %w", err)
	}
	effectiveRampUpDomains = effectiveRampUpDomains.Union(rampUpDomains)
	if effectiveRampUpDomains.Len() == 0 {
		return floor, nil
	}

	affectedNUMAs := affectedRampUpNUMAs(effectiveRampUpDomains, p.machine.machineInfo.CPUTopology, immutablePerNUMA)

	// Read the committed reclaim pool from the same candidateEntries snapshot used
	// for ramp-up domain resolution above, so this plan does not mix a live
	// reclaim revision with the snapshot it derives the rest of the floor from.
	currentReclaim := committedReclaimPoolCPUs(candidateEntries)

	ratio := initialRampUpReclaimCPUSetRatioFromConfig(dynamicConf)
	numaIDs := p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt()
	eligibleByNUMA := make(map[int]machine.CPUSet, len(numaIDs))
	availableByNUMA := make(map[int]int, len(numaIDs))
	reservedFloorByNUMA := make(map[int]machine.CPUSet, len(numaIDs))
	totalEligible := 0
	for _, numaID := range numaIDs {
		numaState := machineState[numaID]
		if numaState == nil {
			return machine.NewCPUSet(), fmt.Errorf("derive ramp-up reclaim floor: missing machine state for NUMA %d", numaID)
		}
		eligible := numaState.GetAvailableCPUSet(p.config.reservedCPUs)
		reservedFloor := p.reclaim.reservedReclaimedCPUSet.Intersection(eligible)
		eligibleByNUMA[numaID] = eligible
		availableByNUMA[numaID] = eligible.Size()
		reservedFloorByNUMA[numaID] = reservedFloor
		totalEligible += eligible.Size()
	}

	// The attempt config is the single frozen dynamic source captured at the
	// outer attempt entry: it carries both the reclaim floor scalar
	// (MinReclaimedResourceForAllocate, which may be nil so the reclaim utils
	// fall back to the static reserve) and the ramp-up ratio, NUMA knobs and
	// enablement gate. Keeping both reads off this one immutable pointer keeps
	// this path consistent with isRampUpReclaimHardPartitionEnabled.
	floorConf := dynamicConf
	ratioConf := dynamicConf

	// preserve p.reclaim.reservedReclaimedCPUsSize as the fallback floor when the global
	// scalar is unset, matching the pre-unification default. the config-aware
	// scalar util keeps the three-state override semantics aligned with the
	// per-NUMA reclaim targets derived below.
	configuredFloor := machine.ResolveConfiguredReclaimFloorFromConfig(
		floorConf, p.machine.machineInfo.CPUTopology, p.reclaim.reservedReclaimedCPUsSize)

	targetByNUMA := make(map[int]int, len(numaIDs))
	if immutablePerNUMA {
		// feed the resolved floor as the fallback so the util derives per-NUMA
		// targets from the canonical ratio config while honoring the reclaim floor
		// scalar owned by floorConf.
		targetByNUMA, err = machine.ResolveHardPartitionReclaimTargets(
			ratioConf,
			p.machine.machineInfo.CPUTopology,
			configuredFloor,
			func(numaID int) int { return reservedFloorByNUMA[numaID].Size() },
			func(numaID int) int { return availableByNUMA[numaID] },
		)
		if err != nil {
			return machine.NewCPUSet(), fmt.Errorf("distribute configured ramp-up reclaim floor: %w", err)
		}
	} else {
		minimumPerNUMA := minimumHardReclaimCoresPerNUMA * p.machine.machineInfo.CPUTopology.CPUsPerCore()
		// The balanced distribution is scoped to the NUMAs an active ramp-up
		// domain actually covers. A NUMA with no local ramp-up must not dilute
		// the ratio-derived global target: distributing the target over all
		// NUMAs would hand an unaffected NUMA's share to the affected NUMAs,
		// over-provisioning the floor. For a pure global-domain overlap ramp-up
		// affectedNUMAs covers every real NUMA, so this narrows back to the
		// legacy node-wide distribution.
		//
		// A NUMA whose eligible capacity cannot yield a single complete core
		// (e.g. fully occupied by dedicated / non-exclusive DNB workloads) has no
		// room for a reclaim reserve; keep its target at 0 and exclude it from the
		// balanced distribution so DistributeNUMATarget's per-NUMA minimum guard
		// does not fail admission closed on it. this mirrors the immutable path,
		// where a zero-core-eligible NUMA also resolves to a zero target.
		distributableByNUMA := make(map[int]int, len(availableByNUMA))
		affectedEligible := 0
		for numaID, avail := range availableByNUMA {
			if !affectedNUMAs.Has(numaID) {
				targetByNUMA[numaID] = 0
				continue
			}
			affectedEligible += avail
			if avail < minimumPerNUMA {
				targetByNUMA[numaID] = 0
				continue
			}
			distributableByNUMA[numaID] = avail
		}
		// The node-wide configured reclaim floor is apportioned to the affected
		// NUMAs by their eligible share: an unaffected NUMA already maintains
		// its steady reclaim contribution, so the ramp-up floor must not heap the
		// whole node-wide minimum onto the subset of ramp-up NUMAs. When every
		// real NUMA is affected (a pure global-domain overlap ramp-up) the
		// scaling factor is 1 and the result matches the legacy node-wide floor.
		minimum := minimumPerNUMA * len(distributableByNUMA)
		scaledConfiguredFloor := 0
		if totalEligible > 0 {
			scaledConfiguredFloor = configuredFloor * affectedEligible / totalEligible
		}
		if scaledConfiguredFloor > minimum {
			minimum = scaledConfiguredFloor
		}
		globalTarget := machine.CalculateGlobalRampUpReclaimTarget(affectedEligible, ratio, minimum)
		if len(distributableByNUMA) > 0 {
			distributed, err := machine.DistributeNUMATarget(
				distributableByNUMA, globalTarget, minimumPerNUMA)
			if err != nil {
				return machine.NewCPUSet(), fmt.Errorf("derive ramp-up reclaim floor failed: %w", err)
			}
			for numaID, target := range distributed {
				targetByNUMA[numaID] = target
			}
		}
	}

	// NUMAs already owned by a committed steady exclusive DNB keep their
	// finalized steady reclaim reserve once ramp-up ends, regardless of
	// reclaimability. The node-level ramp-up floor must not re-impose the
	// ratio-derived immutable target on them; doing so fails admission closed
	// because the eligible reserve (e.g. 2 CPUs) is smaller than the ramp-up
	// target (e.g. 6 CPUs).
	steadyExclusiveNUMAs := candidateEntries.SteadyExclusiveNUMAs(p.machine.machineInfo.CPUTopology)

	for _, numaID := range numaIDs {
		// The ramp-up reclaim floor is only imposed on NUMAs that an active
		// ramp-up domain actually covers. A NUMA without local ramp-up (e.g. a
		// NUMA-exclusive region) keeps its steady state and is left untouched.
		if steadyExclusiveNUMAs.Has(numaID) || !affectedNUMAs.Has(numaID) {
			continue
		}
		eligible := eligibleByNUMA[numaID]
		reservedFloor := reservedFloorByNUMA[numaID]
		target := targetByNUMA[numaID]
		if eligible.Size() < target {
			targetKind := "available"
			if immutablePerNUMA {
				targetKind = "immutable"
			}
			return machine.NewCPUSet(), fmt.Errorf(
				"derive ramp-up reclaim floor for NUMA %d: eligible capacity %d is smaller than %s target %d",
				numaID, eligible.Size(), targetKind, target)
		}
		// reservedReclaimedCPUSet is identity-bearing configuration, not merely
		// a target count. Preserve those exact CPUs first; a positive ratio may
		// add CPUs while preferring the live reclaim set.
		if reservedFloor.Size() > target {
			return machine.NewCPUSet(), fmt.Errorf(
				"derive ramp-up reclaim floor for NUMA %d: reserved floor %d exceeds balanced target %d",
				numaID, reservedFloor.Size(), target)
		}
		floorInNUMA := reservedFloor
		if immutablePerNUMA && !floorInNUMA.IsEmpty() {
			completedReservedFloor, err := completeCoresForCPUSet(
				p.machine.machineInfo.CPUTopology, floorInNUMA)
			if err != nil {
				return machine.NewCPUSet(), fmt.Errorf(
					"complete reserved ramp-up reclaim floor for NUMA %d failed: %w", numaID, err)
			}
			if !completedReservedFloor.IsSubsetOf(eligible) {
				return machine.NewCPUSet(), fmt.Errorf(
					"derive ramp-up reclaim floor for NUMA %d: reserved floor core siblings %s are outside eligible cpus %s",
					numaID, completedReservedFloor.Difference(eligible).String(), eligible.String())
			}
			floorInNUMA = completedReservedFloor
		}
		additional := target - floorInNUMA.Size()
		if additional < 0 {
			return machine.NewCPUSet(), fmt.Errorf(
				"derive ramp-up reclaim floor for NUMA %d: core-aligned reserved floor %d exceeds target %d",
				numaID, floorInNUMA.Size(), target)
		}
		if additional > 0 {
			additionalEligible := eligible.Difference(floorInNUMA)
			preferred := currentReclaim.Intersection(additionalEligible)
			var supplement machine.CPUSet
			if immutablePerNUMA {
				supplement = takeCoreAlignedCPUSet(
					p.machine.machineInfo.CPUTopology, additionalEligible, preferred, additional)
				if supplement.Size() != additional {
					return machine.NewCPUSet(), fmt.Errorf(
						"select core-aligned ramp-up reclaim floor for NUMA %d: selected %d cpus, target is %d",
						numaID, supplement.Size(), additional)
				}
			} else {
				var err error
				supplement, err = p.takeByTopologyPreferring(additionalEligible, preferred, additional)
				if err != nil {
					return machine.NewCPUSet(), fmt.Errorf("select ramp-up reclaim floor for NUMA %d failed: %w", numaID, err)
				}
			}
			floorInNUMA = floorInNUMA.Union(supplement)
		}
		floor = floor.Union(floorInNUMA)
	}
	return floor, nil
}

// globalRampUpDomainActive reports whether the global (non-NUMA-binding)
// ramp-up domain (FakedNUMAID) is among the active ramp-up domains.
func globalRampUpDomainActive(rampUpDomains sets.Int) bool {
	return rampUpDomains.Has(commonstate.FakedNUMAID)
}

// affectedRampUpNUMAs resolves which real NUMAs must receive the ramp-up
// reclaim floor from the active ramp-up domains.
//
// In immutable (dedicated/reclaim disjoint) mode only a real NUMA ramp-up
// domain narrows onto its own NUMA; the global (non-binding) domain lives in
// the fake/reclaim space and must not touch a real NUMA.
//
// In legacy overlap mode a real NUMA domain still narrows onto itself, while
// the global domain keeps the historical node-wide propagation onto every real
// NUMA.
func affectedRampUpNUMAs(rampUpDomains sets.Int, topology *machine.CPUTopology, immutablePerNUMA bool) sets.Int {
	affected := sets.NewInt()
	if topology == nil || rampUpDomains == nil {
		return affected
	}
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		if rampUpDomains.Has(numaID) {
			affected.Insert(numaID)
		}
	}
	if !immutablePerNUMA && globalRampUpDomainActive(rampUpDomains) {
		for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
			affected.Insert(numaID)
		}
	}
	return affected
}

// activeRampUpDomainsFromEntries derives the active ramp-up reclaim domains
// currently recorded in the pod entries.
func (p *DynamicPolicy) activeRampUpDomainsFromEntries(entries state.PodEntries) (sets.Int, error) {
	if p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return nil, fmt.Errorf("activeRampUpDomainsFromEntries got nil cpu topology")
	}
	return entries.ActiveRampUpDomains(p.machine.machineInfo.CPUTopology)
}

// rampUpDomainForAllocation resolves the reclaim domain(s) a single entering
// non-NUMA-binding allocation would activate. NUMA-binding entering requests
// are resolved by enteringNUMABindingRampUpDomains from their hint nodes, since
// the request side has no committed placement yet.
func (p *DynamicPolicy) rampUpDomainForAllocation(allocationInfo *state.AllocationInfo) (sets.Int, error) {
	if p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return nil, fmt.Errorf("rampUpDomainForAllocation got nil cpu topology")
	}
	if allocationInfo == nil {
		return sets.NewInt(), nil
	}
	return allocationInfo.RampUpReclaimDomains(p.machine.machineInfo.CPUTopology)
}

// enteringNUMABindingRampUpDomains resolves the reclaim domains of the NUMA-binding
// request currently being placed. Unlike committed ramp-up allocations (which are
// pinned to a single NUMA), an in-flight request may be distributed across several
// hinted NUMAs; each hinted NUMA becomes its own reclaim domain. A non-binding
// request maps to the global domain. A request with no hinted NUMAs fails closed.
func enteringNUMABindingRampUpDomains(annotations map[string]string, hintNodes []uint64, topology *machine.CPUTopology) (sets.Int, error) {
	if topology == nil {
		return nil, fmt.Errorf("entering numa-binding ramp-up domains: missing topology")
	}
	meta := &commonstate.AllocationMeta{Annotations: annotations}
	if !meta.CheckNUMABinding() {
		return sets.NewInt(commonstate.FakedNUMAID), nil
	}
	domains := sets.NewInt()
	for _, node := range hintNodes {
		id := int(node)
		if !topology.CPUDetails.NUMANodes().Contains(id) {
			return nil, fmt.Errorf(
				"out-of-range ramp-up domain: entering numa-binding hint node %d is not in topology", id)
		}
		domains.Insert(id)
	}
	if domains.Len() == 0 {
		return nil, fmt.Errorf(
			"missing ramp-up domain: numa-binding entering allocation has no hint nodes")
	}
	return domains, nil
}

func (p *DynamicPolicy) deriveSteadyReclaimFloor(
	reclaimEligiblePerNUMA map[int]machine.CPUSet,
) (machine.CPUSet, error) {
	if !p.isRampUpReclaimHardPartitionEnabled() || p.machine.machineInfo == nil {
		return machine.NewCPUSet(), nil
	}

	var dynamicConf *dynamicconfig.Configuration
	if p.config.dynamicConfig != nil {
		dynamicConf = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	targetByNUMA := machine.ResolvePerNUMAReservedForReclaim(dynamicConf, p.machine.machineInfo.CPUTopology)
	currentReclaim := machine.NewCPUSet()
	if reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName); reclaimInfo != nil {
		currentReclaim = reclaimInfo.AllocationResult
	}

	floor := machine.NewCPUSet()
	for _, numaID := range p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
		eligible, found := reclaimEligiblePerNUMA[numaID]
		if !found {
			return machine.NewCPUSet(), fmt.Errorf("derive steady reclaim floor: missing reclaim eligibility for NUMA %d", numaID)
		}
		target := targetByNUMA[numaID]

		reservedIdentities := p.reclaim.reservedReclaimedCPUSet.Intersection(
			p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID))
		floorInNUMA, err := selectAdmissionSteadyReclaimTarget(
			p.machine.machineInfo.CPUTopology,
			eligible,
			currentReclaim.Intersection(eligible),
			reservedIdentities,
			target,
		)
		if err != nil {
			return machine.NewCPUSet(), fmt.Errorf("derive steady reclaim floor for NUMA %d: %w", numaID, err)
		}
		if floorInNUMA.Size() < target {
			general.InfoS("reclaimTargetShortfall",
				"numaID", numaID,
				"requestedReclaimQuantity", target,
				"actualReclaimQuantity", floorInNUMA.Size(),
				"remainderForDedicated", target-floorInNUMA.Size(),
				"reason", "whole_core_capacity")
		}
		floor = floor.Union(floorInNUMA)
	}
	return floor, nil
}

func selectAdmissionSteadyReclaimTarget(
	topology *machine.CPUTopology,
	eligible machine.CPUSet,
	preferred machine.CPUSet,
	mandatory machine.CPUSet,
	softTarget int,
) (machine.CPUSet, error) {
	completedMandatory, err := completeEligibleCoresForPreferredCPUSet(topology, eligible, mandatory)
	if err != nil {
		return machine.NewCPUSet(), err
	}
	if !completedMandatory.IsSubsetOf(eligible) {
		return machine.NewCPUSet(), fmt.Errorf(
			"mandatory reclaim CPUs %s are outside eligibility %s",
			completedMandatory.String(), eligible.String())
	}

	// Mandatory identities are a hard floor, while softTarget only caps optional
	// supplementation; completing a mandatory sibling core may exceed that cap.
	selected := completedMandatory.Clone()
	budget := softTarget - selected.Size()
	if budget <= 0 {
		return selected, nil
	}
	supplementEligible := eligible.Difference(selected)
	supplement := selectAdmissionCoreAlignedSupplement(
		topology,
		supplementEligible,
		preferred.Intersection(supplementEligible),
		budget,
	)
	return selected.Union(supplement), nil
}

type admissionCoreSelection struct {
	cpus         machine.CPUSet
	preferredHit int
	keys         []physicalCoreKey
}

// selectAdmissionCoreAlignedSupplement uses a NUMA-local, budget-bounded
// knapsack. Mixed-SMT cores may have different logical CPU capacities, so a
// greedy walk can leave usable capacity behind. Selection priorities are:
// maximum capacity not exceeding budget, maximum preferred siblings retained,
// then the lexicographically smallest physical-core key sequence.
func selectAdmissionCoreAlignedSupplement(
	topology *machine.CPUTopology,
	eligible machine.CPUSet,
	preferred machine.CPUSet,
	budget int,
) machine.CPUSet {
	if topology == nil || budget <= 0 || eligible.IsEmpty() {
		return machine.NewCPUSet()
	}

	cores := coreAlignedCandidates(topology, eligible, preferred)
	sort.Slice(cores, func(i, j int) bool {
		return physicalCoreKeyLess(cores[i].key, cores[j].key)
	})
	totalCapacity := 0
	for _, core := range cores {
		totalCapacity += core.cpus.Size()
	}
	if budget > totalCapacity {
		budget = totalCapacity
	}

	dp := make([]*admissionCoreSelection, budget+1)
	dp[0] = &admissionCoreSelection{cpus: machine.NewCPUSet()}
	for _, core := range cores {
		capacity := core.cpus.Size()
		if capacity > budget {
			continue
		}
		for used := budget; used >= capacity; used-- {
			previous := dp[used-capacity]
			if previous == nil {
				continue
			}
			candidate := &admissionCoreSelection{
				cpus:         previous.cpus.Union(core.cpus),
				preferredHit: previous.preferredHit + core.preferredHit,
				keys:         appendCoreKey(previous.keys, core.key),
			}
			if betterAdmissionCoreSelection(candidate, dp[used]) {
				dp[used] = candidate
			}
		}
	}
	for used := budget; used >= 0; used-- {
		if dp[used] != nil {
			return dp[used].cpus
		}
	}
	return machine.NewCPUSet()
}

func appendCoreKey(keys []physicalCoreKey, key physicalCoreKey) []physicalCoreKey {
	appended := make([]physicalCoreKey, len(keys)+1)
	copy(appended, keys)
	appended[len(keys)] = key
	return appended
}

func betterAdmissionCoreSelection(candidate, current *admissionCoreSelection) bool {
	if current == nil {
		return true
	}
	if candidate.preferredHit != current.preferredHit {
		return candidate.preferredHit > current.preferredHit
	}
	for i := 0; i < len(candidate.keys) && i < len(current.keys); i++ {
		if candidate.keys[i] == current.keys[i] {
			continue
		}
		return physicalCoreKeyLess(candidate.keys[i], current.keys[i])
	}
	return len(candidate.keys) < len(current.keys)
}

// numaBindingPartitionEligibility applies the same resource-package owner rules
// as the advisor block planner to each hinted NUMA's currently available CPUs.
func (p *DynamicPolicy) numaBindingPartitionEligibility(
	machineState state.NUMANodeMap,
	resourcePackageName string,
	hintNodes []uint64,
) (map[int]machine.CPUSet, map[int]machine.CPUSet, error) {
	rpPinnedCPUSet := make(map[string]machine.CPUSet)
	for _, numaState := range machineState {
		if numaState == nil {
			continue
		}
		for resourcePackage, rpState := range numaState.ResourcePackageStates {
			if rpState != nil && !rpState.PinnedCPUSet.IsEmpty() {
				rpPinnedCPUSet[resourcePackage] = rpPinnedCPUSet[resourcePackage].Union(rpState.PinnedCPUSet)
			}
		}
	}
	allPinnedCPUs := machine.NewCPUSet()
	for _, pinnedCPUs := range rpPinnedCPUSet {
		allPinnedCPUs = allPinnedCPUs.Union(pinnedCPUs)
	}

	selectorText := p.config.dynamicConfig.GetDynamicConfiguration().DisableReclaimPinnedCPUSetResourcePackageSelector
	disableReclaimSelector, err := general.ParseSelector(selectorText)
	if err != nil {
		return nil, nil, fmt.Errorf("parse disable-reclaim resource package selector: %w", err)
	}
	nonNilMachineState := make(state.NUMANodeMap, len(machineState))
	for numaID, numaState := range machineState {
		if numaState != nil {
			nonNilMachineState[numaID] = numaState
		}
	}
	nonReclaimableCPUSet := cpuutil.GetAggResourcePackagePinnedCPUSet(disableReclaimSelector, nonNilMachineState)

	dedicatedEligiblePerNUMA := make(map[int]machine.CPUSet, len(hintNodes))
	reclaimEligiblePerNUMA := make(map[int]machine.CPUSet, len(hintNodes))
	for _, hintedNUMAID := range hintNodes {
		numaID := int(hintedNUMAID)
		numaState := machineState[numaID]
		if numaState == nil {
			return nil, nil, fmt.Errorf("missing machine state for hinted NUMA %d", numaID)
		}
		scope := numaState.GetAvailableCPUSet(p.config.reservedCPUs)
		dedicatedEligiblePerNUMA[numaID] = advisorBlockOwnerEligible(
			commonstate.PoolNameDedicated,
			resourcePackageName,
			scope,
			allPinnedCPUs,
			rpPinnedCPUSet,
			nonReclaimableCPUSet,
		)
		reclaimEligiblePerNUMA[numaID] = advisorBlockOwnerEligible(
			commonstate.PoolNameReclaim,
			"",
			scope,
			allPinnedCPUs,
			rpPinnedCPUSet,
			nonReclaimableCPUSet,
		)
	}
	return dedicatedEligiblePerNUMA, reclaimEligiblePerNUMA, nil
}

// selectNumaBindingReclaimPartition preserves mandatory reclaim identities and
// fills the caller-selected steady or ramp-up target within each hinted NUMA.
func (p *DynamicPolicy) selectNumaBindingReclaimPartition(
	derivedFloor machine.CPUSet,
	dedicatedEligiblePerNUMA, reclaimEligiblePerNUMA map[int]machine.CPUSet,
	hintNodes []uint64,
	coverExclusivePartition bool,
) (machine.CPUSet, error) {
	return p.selectNumaBindingReclaimPartitionWithPreference(
		derivedFloor,
		dedicatedEligiblePerNUMA,
		reclaimEligiblePerNUMA,
		hintNodes,
		coverExclusivePartition,
		nil,
	)
}

// committedReclaimPoolCPUs is a pure, best-effort read of the committed reclaim
// pool CPUs out of an owned PodEntries snapshot. It never touches live state:
// the caller passes the exact snapshot to read, keeping the committed pool
// distinct from the pending/final entries under construction and from the cgroup
// view. A missing reclaim pool entry (first initialization, or a best-effort
// preservation path) yields an empty set; readiness and existence checks must
// inspect the snapshot themselves rather than rely on this fallback. The
// returned CPUSet is an independent clone, mirroring PodEntries.GetCPUSetForPool.
func committedReclaimPoolCPUs(entries state.PodEntries) machine.CPUSet {
	cset, err := entries.GetCPUSetForPool(commonstate.PoolNameReclaim)
	if err != nil {
		return machine.NewCPUSet()
	}
	return cset
}

// anchorExclusiveReclaimFloorToLivePool re-anchors the hinted-NUMA portion of the
// selected hard reclaim floor onto the committed reclaim pool resident on each
// hinted NUMA. The committed pool is read from the caller-owned snapshot, not
// from live state, so the anchor shares one state generation with the rest of
// this admission. The pool is already core-aligned; only cores that are entirely
// reclaim-eligible on the NUMA are kept, so a core whose sibling is protected (the
// HP / non-reclaimable gate) is skipped whole rather than forcing the protected
// sibling back into the floor. A hinted NUMA with no eligible live core keeps
// its derived floor untouched. Non-hinted NUMAs are left as derived.
func (p *DynamicPolicy) anchorExclusiveReclaimFloorToLivePool(
	floor machine.CPUSet,
	hintNodes []uint64,
	reclaimEligiblePerNUMA map[int]machine.CPUSet,
	committedEntries state.PodEntries,
) machine.CPUSet {
	live := committedReclaimPoolCPUs(committedEntries)
	allCores := physicalCoreCPUs(p.machine.machineInfo.CPUTopology)
	anchored := floor.Clone()
	for _, numaID := range hintNodes {
		numaCPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(int(numaID))
		liveOnNUMA := live.Intersection(numaCPUs)
		if liveOnNUMA.IsEmpty() {
			continue
		}
		eligible := reclaimEligiblePerNUMA[int(numaID)]
		liveFloor := machine.NewCPUSet()
		for _, cpu := range liveOnNUMA.ToSliceInt() {
			info, ok := p.machine.machineInfo.CPUTopology.CPUDetails[cpu]
			if !ok {
				continue
			}
			siblings := allCores[physicalCoreKeyForCPU(info)]
			if siblings.IsSubsetOf(eligible) {
				liveFloor = liveFloor.Union(siblings)
			}
		}
		if liveFloor.IsEmpty() {
			continue
		}
		derivedOnNUMA := floor.Intersection(numaCPUs)
		// Only re-anchor when the live reclaim has relocated off the derived floor:
		// if the live cores are already part of the derived floor, the existing
		// partition (reserved identity plus supplement) already preserves them and
		// must not be shrunk.
		if liveFloor.IsSubsetOf(derivedOnNUMA) {
			continue
		}
		// A relocation preserves the per-NUMA reclaim core count: the live floor
		// must exactly replace the derived anchor. When the live pool is larger
		// than the derived floor it is a separate/bigger reclaim budget (the
		// derivation's configured steady target governs it), not a relocation, and
		// overriding it would under-allocate the DNB against a deliberate shrink.
		if liveFloor.Size() != derivedOnNUMA.Size() {
			continue
		}
		// Drop the derived anchor on this NUMA and anchor on the live reclaim cores.
		anchored = anchored.Difference(numaCPUs).Union(liveFloor)
	}
	return anchored
}

func (p *DynamicPolicy) selectNumaBindingReclaimPartitionWithPreference(
	derivedFloor machine.CPUSet,
	dedicatedEligiblePerNUMA, reclaimEligiblePerNUMA map[int]machine.CPUSet,
	hintNodes []uint64,
	coverExclusivePartition bool,
	preference *numaBindingAllocationPreference,
) (machine.CPUSet, error) {
	if !p.isRampUpReclaimHardPartitionEnabled() || !coverExclusivePartition {
		return derivedFloor, nil
	}

	hintedNUMAs := sets.NewInt()
	hintedCPUs := machine.NewCPUSet()
	for _, numaID := range hintNodes {
		hintedNUMAs.Insert(int(numaID))
		hintedCPUs = hintedCPUs.Union(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(int(numaID)))
	}
	selected := derivedFloor.Difference(hintedCPUs)
	for numaID, reclaimEligible := range reclaimEligiblePerNUMA {
		dedicatedEligible := dedicatedEligiblePerNUMA[numaID]

		numaCPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID)
		derivedInNUMA := derivedFloor.Intersection(numaCPUs)
		if !hintedNUMAs.Has(numaID) {
			if !derivedInNUMA.IsSubsetOf(reclaimEligible) {
				return machine.NewCPUSet(), fmt.Errorf(
					"NUMA %d selected reclaim CPUs %s are not a subset of reclaim eligibility %s",
					numaID, derivedInNUMA.String(), reclaimEligible.String())
			}
			continue
		}

		reclaimOnly := reclaimEligible.Difference(dedicatedEligible)
		steadyReserve := p.reclaim.reservedReclaimedCPUSet.Intersection(numaCPUs)
		completedSteadyReserve := machine.NewCPUSet()
		if preference == nil {
			completed, completeErr := completeEligibleCoresForPreferredCPUSet(
				p.machine.machineInfo.CPUTopology, reclaimEligible, steadyReserve)
			if completeErr != nil {
				return machine.NewCPUSet(), fmt.Errorf(
					"select eligible steady reclaim reserve for NUMA %d: %w", numaID, completeErr)
			}
			completedSteadyReserve = completed
			if completedSteadyReserve.Size() > derivedInNUMA.Size() {
				return machine.NewCPUSet(), fmt.Errorf(
					"NUMA %d mandatory reclaim CPUs exceed selected floor: mandatory=%d target=%d",
					numaID, completedSteadyReserve.Size(), derivedInNUMA.Size())
			}
		}
		mandatory, err := completeCoresForCPUSet(
			p.machine.machineInfo.CPUTopology, reclaimOnly.Union(completedSteadyReserve))
		if err != nil {
			return machine.NewCPUSet(), fmt.Errorf("complete mandatory reclaim cores for NUMA %d: %w", numaID, err)
		}
		if !mandatory.IsSubsetOf(reclaimEligible) {
			return machine.NewCPUSet(), fmt.Errorf(
				"NUMA %d mandatory reclaim CPUs %s are not a subset of reclaim eligibility %s",
				numaID, mandatory.String(), reclaimEligible.String())
		}

		target := general.Max(mandatory.Size(), derivedInNUMA.Size())
		if target > reclaimEligible.Size() {
			return machine.NewCPUSet(), fmt.Errorf(
				"NUMA %d reclaim eligibility %d is smaller than required reserve %d",
				numaID, reclaimEligible.Size(), target)
		}

		selectedInNUMA := mandatory
		remaining := target - selectedInNUMA.Size()
		if remaining > 0 {
			candidates := reclaimEligible.Difference(selectedInNUMA)
			var supplement machine.CPUSet
			if preference == nil {
				supplement = selectAdmissionCoreAlignedSupplement(
					p.machine.machineInfo.CPUTopology,
					candidates,
					derivedFloor.Intersection(candidates),
					remaining,
				)
			} else {
				supplement = takeReclaimSupplementWithPreference(
					p.machine.machineInfo.CPUTopology, candidates, remaining, preference)
			}
			// The target is a soft ceiling after mandatory replacement. Mixed-SMT
			// cores may not compose the exact remainder, so keep the largest
			// core-aligned supplement that fits instead of rejecting admission.
			selectedInNUMA = selectedInNUMA.Union(supplement)
		}
		selected = selected.Union(selectedInNUMA)
	}
	if err := assertCoreAligned(selected, p.machine.machineInfo.CPUTopology); err != nil {
		return machine.NewCPUSet(), fmt.Errorf("select NUMA binding reclaim partition: %w", err)
	}
	return selected, nil
}

func (p *DynamicPolicy) podEnableReclaim(ctx context.Context, podUID string) (bool, error) {
	return resourcehelper.PodEnableReclaim(ctx, p.meta.metaServer, podUID, p.isReclaimEnabled())
}

func (p *DynamicPolicy) podEnableReclaimForNumaBindingAllocation(
	ctx context.Context, req *pluginapi.ResourceRequest,
) (bool, error) {
	originalPodMeta, ok := allocationPodMetaFromContext(ctx)
	if !ok {
		originalPodMeta = podMetaFromResourceRequest(req)
	}

	if p.isRampUpReclaimHardPartitionEnabled() &&
		qosutil.AnnotationsIndicateNUMAExclusive(originalPodMeta.Annotations) &&
		p.state.GetDisableDedicatedCoresOverlapReclaimedCores() {
		// The immutable request metadata is authoritative for the adapter-owned
		// performance level and is available before the Pod informer converges.
		metadataEnabled, err := resourcehelper.PodMetaPerformanceLevelEnableReclaim(
			ctx, p.meta.metaServer, originalPodMeta, p.isReclaimEnabled())
		if err != nil || !metadataEnabled {
			return metadataEnabled, err
		}

		// Baseline ordering requires CreationTimestamp, which ResourceRequest
		// does not carry. Fetch the complete Pod only after the fast path has
		// ruled out a definitively non-reclaimable performance level.
		pod, err := p.meta.metaServer.GetPod(ctx, req.PodUid)
		if err != nil {
			if metapod.IsPodNotFound(err) {
				general.Warningf("allocateNumaBindingCPUs: pod %s not found, fallback podReclaimEnabled=true: %v",
					req.PodUid, err)
				return true, nil
			}
			return false, err
		}
		if pod == nil {
			return false, fmt.Errorf("get pod %s returned nil", req.PodUid)
		}
		return resourcehelper.PodMetaBaselineEnableReclaim(ctx, p.meta.metaServer, pod.ObjectMeta)
	}

	return p.podEnableReclaimOrFallback(ctx, req.PodUid, "allocateNumaBindingCPUs"), nil
}

// podEnableReclaimOrFallback keeps the legacy best-effort behavior for paths
// that do not require an authoritative hard-partition admission decision.
func (p *DynamicPolicy) podEnableReclaimOrFallback(ctx context.Context, podUID, operation string) bool {
	podReclaimEnabled, err := p.podEnableReclaim(ctx, podUID)
	if err != nil {
		general.Warningf("%s: failed to check pod enable reclaim for pod %s, fallback podReclaimEnabled=false: %v",
			operation, podUID, err)
		return false
	}
	return podReclaimEnabled
}

func initialRampUpReclaimCPUSetRatioFromConfig(dyn *dynamicconfig.Configuration) float64 {
	if dyn == nil {
		return 0
	}
	return dyn.InitialRampUpReclaimCPUSetRatio
}
