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
	stderrors "errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuburst"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuidle"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuweight"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupcm "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	metricsNamePodTotalRequestLargerThanBindingCPUSet = "pod_total_request_larger_than_cpu_set"

	defaultSystemExclusivePoolShrinkRatio = 0.15
	defaultSystemExclusivePoolShrinkMin   = 2
	defaultSystemExclusivePoolShrinkMax   = 8
)

// cpuSetHealthOps groups the per-instance cgroup reads used by checkCPUSet.
// A nil pointer lazily binds to the production functions. Tests install a fake
// so the health check can run under t.Parallel without monkey patching.
type cpuSetHealthOps struct {
	getContainerAbsCgroupPath func(subsys, podUID, containerID string) (string, error)
	getCPUSetWithAbsolutePath func(absPath string) (*cgroupcm.CPUSetStats, error)
	updateHealthzState        func(state general.HealthzCheckState, message string) error
	updateHealthzStateByError func(err error) error
}

func defaultCPUSetHealthOps() *cpuSetHealthOps {
	return &cpuSetHealthOps{
		getContainerAbsCgroupPath: cgroupcm.GetContainerAbsCgroupPath,
		getCPUSetWithAbsolutePath: cgroupcmutils.GetCPUSetWithAbsolutePath,
		updateHealthzState: func(state general.HealthzCheckState, message string) error {
			return general.UpdateHealthzState(cpuconsts.CheckCPUSet, state, message)
		},
		updateHealthzStateByError: func(err error) error {
			return general.UpdateHealthzStateByError(cpuconsts.CheckCPUSet, err)
		},
	}
}

func (p *DynamicPolicy) cpuHealthOps() *cpuSetHealthOps {
	if p.cpuSetHealthOps == nil {
		p.cpuSetHealthOps = defaultCPUSetHealthOps()
	}
	return p.cpuSetHealthOps
}

// checkCPUSet emit errors if the memory allocation falls into unexpected results
func (p *DynamicPolicy) checkCPUSet(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec checkCPUSet")
	var (
		errList       []error
		invalidCPUSet = false
		cpuSetOverlap = false
	)

	defer func() {
		healthOps := p.cpuHealthOps()
		if len(errList) > 0 {
			_ = healthOps.updateHealthzStateByError(utilerrors.NewAggregate(errList))
		} else if invalidCPUSet {
			_ = healthOps.updateHealthzState(general.HealthzCheckStateNotReady, "invalid cpuset exists")
		} else if cpuSetOverlap {
			_ = healthOps.updateHealthzState(general.HealthzCheckStateNotReady, "cpuset overlap")
		} else {
			_ = healthOps.updateHealthzState(general.HealthzCheckStateReady, "")
		}
	}()

	podEntries := p.state.GetPodEntries()
	actualCPUSets := make(map[string]map[string]machine.CPUSet)
	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil || !allocationInfo.CheckMainContainer() {
				continue
			} else if allocationInfo.CheckShared() && p.getContainerRequestedCores(allocationInfo) == 0 {
				general.Warningf("skip cpuset checking for pod: %s/%s container: %s with zero cpu request",
					allocationInfo.PodNamespace, allocationInfo.PodName, containerName)
				continue
			}

			tags := metrics.ConvertMapToTags(map[string]string{
				"podNamespace":  allocationInfo.PodNamespace,
				"podName":       allocationInfo.PodName,
				"containerName": allocationInfo.ContainerName,
			})
			var (
				containerId string
				cpuSetStats *cgroupcm.CPUSetStats
			)

			containerId, err := p.meta.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id of pod: %s container: %s failed with error: %v", podUID, containerName, err)
				continue
			}

			cpusetAbsCGPath, err := p.cpuHealthOps().getContainerAbsCgroupPath(cgroupcm.CgroupSubsysCPUSet, podUID, containerId)
			if err != nil {
				general.Errorf("get container abs cgroup path of pod: %s container: %s failed with error: %v", podUID, containerName, err)
				_ = p.emitter.emitter.StoreInt64(util.MetricNameCgroupPathNotFound, 1, metrics.MetricTypeNameRaw, tags...)
				continue
			}

			cpuSetStats, err = p.cpuHealthOps().getCPUSetWithAbsolutePath(cpusetAbsCGPath)
			if err != nil {
				general.Errorf("GetCPUSet of pod: %s container: name(%s), id(%s) failed with error: %v",
					podUID, containerName, containerId, err)
				_ = p.emitter.emitter.StoreInt64(util.MetricNameRealStateInvalid, 1, metrics.MetricTypeNameRaw, tags...)
				errList = append(errList, err)
				continue
			}

			if actualCPUSets[podUID] == nil {
				actualCPUSets[podUID] = make(map[string]machine.CPUSet)
			}
			actualCPUSets[podUID][containerName] = machine.MustParse(cpuSetStats.CPUs)

			general.Infof("pod: %s/%s, container: %s, state CPUSet: %s, actual CPUSet: %s",
				allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
				allocationInfo.AllocationResult.String(), actualCPUSets[podUID][containerName].String())

			// only do comparison for dedicated_cores with numa_biding to avoid effect of adjustment for shared_cores
			if !allocationInfo.CheckDedicated() {
				continue
			}

			if !actualCPUSets[podUID][containerName].Equals(allocationInfo.OriginalAllocationResult) {
				invalidCPUSet = true
				general.Errorf("pod: %s/%s, container: %s, cpuset invalid",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				_ = p.emitter.emitter.StoreInt64(util.MetricNameCPUSetInvalid, 1, metrics.MetricTypeNameRaw, tags...)
			}
		}
	}

	unionDedicatedCPUSet := machine.NewCPUSet()
	unionSharedCPUSet := machine.NewCPUSet()

	for podUID, containerEntries := range actualCPUSets {
		for containerName, cset := range containerEntries {
			allocationInfo := podEntries[podUID][containerName]
			if allocationInfo == nil {
				continue
			}
			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				if !cpuSetOverlap && cset.Intersection(unionDedicatedCPUSet).Size() != 0 {
					cpuSetOverlap = true
					general.Errorf("pod: %s/%s, container: %s cpuset: %s overlaps with others",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, cset.String())
				}
				unionDedicatedCPUSet = unionDedicatedCPUSet.Union(cset)
			case consts.PodAnnotationQoSLevelSharedCores:
				unionSharedCPUSet = unionSharedCPUSet.Union(cset)
			}
		}
	}

	regionOverlap := unionSharedCPUSet.Intersection(unionDedicatedCPUSet).Size() != 0
	if regionOverlap {
		general.Errorf("shared_cores union cpuset: %s overlaps with dedicated_cores union cpuset: %s",
			unionSharedCPUSet.String(), unionDedicatedCPUSet.String())
	}

	if !cpuSetOverlap {
		cpuSetOverlap = regionOverlap
	}
	if cpuSetOverlap {
		general.Errorf("found cpuset overlap. actualCPUSets: %+v", actualCPUSets)
		_ = p.emitter.emitter.StoreInt64(util.MetricNameCPUSetOverlap, 1, metrics.MetricTypeNameRaw)
	}

	p.checkCPUSetWithPodTotalRequest(podEntries, actualCPUSets)
	general.Infof("finish checkCPUSet")
}

type cpusetPodState struct {
	cpuset               machine.CPUSet
	totalMilliCPURequest int64
	podUIDs              sets.String
	podMap               map[string]*v1.Pod
}

func (p *DynamicPolicy) checkCPUSetWithPodTotalRequest(
	podEntries state.PodEntries,
	actualCPUSets map[string]map[string]machine.CPUSet,
) {
	ctx := context.Background()
	cpusetPodStateMap := p.buildCPUSetPodStateMap(ctx, podEntries, actualCPUSets)
	p.checkAndEmitMetrics(podEntries, cpusetPodStateMap)
	general.Infof("finish checkCPUSetWithPodTotalRequest")
}

func (p *DynamicPolicy) buildCPUSetPodStateMap(ctx context.Context, podEntries state.PodEntries, actualCPUSets map[string]map[string]machine.CPUSet) map[string]*cpusetPodState {
	cpusetPodStateMap := make(map[string]*cpusetPodState)

	// Build initial cpuset state map
	for podUID, containerCPUSets := range actualCPUSets {
		for _, cset := range containerCPUSets {
			mainContainerEntry := podEntries[podUID].GetMainContainerEntry()
			if mainContainerEntry == nil || mainContainerEntry.CheckReclaimed() {
				continue
			}

			csetStr := cset.String()
			if _, ok := cpusetPodStateMap[csetStr]; !ok {
				cpusetPodStateMap[csetStr] = &cpusetPodState{
					cpuset:  cset,
					podUIDs: sets.NewString(),
				}
			}
			cpusetPodStateMap[csetStr].podUIDs.Insert(podUID)
		}
	}

	// Populate pod info and calculate total CPU requests
	for csetStr, cs := range cpusetPodStateMap {
		podMap := make(map[string]*v1.Pod, cs.podUIDs.Len())
		totalMilliCPURequest := int64(0)

		for _, podUID := range cs.podUIDs.List() {
			pod, err := p.meta.metaServer.GetPod(ctx, podUID)
			if err != nil {
				if metapod.IsPodNotFound(err) {
					general.Infof("pod: %s is already gone, skip cpuset async check", podUID)
					cs.podUIDs.Delete(podUID)
					continue
				}
				general.Errorf("get pod: %s failed with error: %v", podUID, err)
				continue
			}
			if pod == nil {
				general.Infof("pod: %s is nil, skip cpuset async check", podUID)
				cs.podUIDs.Delete(podUID)
				continue
			}

			if !native.PodIsActive(pod) {
				cs.podUIDs.Delete(podUID)
				continue
			}

			resources := native.SumUpPodRequestResources(pod)
			totalMilliCPURequest += resources.Cpu().MilliValue()
			podMap[podUID] = pod
		}

		cs.podMap = podMap
		cs.totalMilliCPURequest = totalMilliCPURequest
		general.Infof("cpuset: %s, size: %d, totalMilliCPURequest: %d, podUIDs: %v", csetStr, cs.cpuset.Size(),
			totalMilliCPURequest, cs.podUIDs.List())
	}

	return cpusetPodStateMap
}

func (p *DynamicPolicy) checkAndEmitMetrics(podEntries state.PodEntries, cpusetPodStateMap map[string]*cpusetPodState) {
	allowSharedCoresOverlapReclaimedCores := p.state.GetAllowSharedCoresOverlapReclaimedCores()

	for cpuset, cs := range cpusetPodStateMap {
		totalMilliCPURequest := p.calculateTotalCPURequest(cpuset, cs, cpusetPodStateMap)
		if cs.cpuset.Size() == 0 || totalMilliCPURequest == 0 {
			continue
		}
		exceededRatio := float64(totalMilliCPURequest-int64(cs.cpuset.Size()*1000)) / float64(totalMilliCPURequest)

		if exceededRatio > 0 {
			p.emitExceededMetrics(podEntries, cpuset, cs, exceededRatio, allowSharedCoresOverlapReclaimedCores)
		}
	}
}

func (p *DynamicPolicy) calculateTotalCPURequest(cpuset string, cs *cpusetPodState, cpusetPodStateMap map[string]*cpusetPodState) int64 {
	totalMilliCPURequest := cs.totalMilliCPURequest

	// Add requests from subsets
	for otherCPUSet, otherState := range cpusetPodStateMap {
		if cpuset == otherCPUSet {
			continue
		}

		if otherState.cpuset.IsSubsetOf(cs.cpuset) {
			totalMilliCPURequest += otherState.totalMilliCPURequest
		}
	}

	return totalMilliCPURequest
}

func (p *DynamicPolicy) emitExceededMetrics(
	podEntries state.PodEntries,
	cpuset string,
	cs *cpusetPodState,
	exceededRatio float64,
	allowSharedCoresOverlapReclaimedCores bool,
) {
	enableReclaim := false
	dynamicConfiguration := p.config.dynamicConfig.GetDynamicConfiguration()
	if dynamicConfiguration != nil && dynamicConfiguration.AdminQoSConfiguration != nil {
		enableReclaim = dynamicConfiguration.EnableReclaim
	}
	for podUID, pod := range cs.podMap {
		mainContainerEntry := podEntries[podUID].GetMainContainerEntry()
		if mainContainerEntry == nil ||
			(mainContainerEntry.CheckShared() && enableReclaim && !allowSharedCoresOverlapReclaimedCores) {
			continue
		}

		// check if the pod exceeds the binding cpuset by more than 1 core
		outOfTolerance := int64(cs.cpuset.Size()+1) < (cs.totalMilliCPURequest / 1000)

		general.Errorf("pod: %s/%s, ownerPoolName: %s, qosLevel: %s, cpuset: %s, size %d, exceeds total cpu request: %.3f, exceeded ratio: %.3f, outOfTolerance: %v",
			pod.Namespace, pod.Name, mainContainerEntry.OwnerPoolName, mainContainerEntry.QoSLevel, cpuset, cs.cpuset.Size(),
			float64(cs.totalMilliCPURequest)/1000, exceededRatio, outOfTolerance)

		_ = p.emitter.emitter.StoreFloat64(metricsNamePodTotalRequestLargerThanBindingCPUSet, exceededRatio, metrics.MetricTypeNameRaw, []metrics.MetricTag{
			{Key: "podNamespace", Val: pod.Namespace},
			{Key: "podName", Val: pod.Name},
			{Key: "qosLevel", Val: mainContainerEntry.QoSLevel},
			{Key: "ownerPoolName", Val: mainContainerEntry.OwnerPoolName},
			{Key: "poolType", Val: commonstate.GetPoolType(mainContainerEntry.OwnerPoolName)},
			{Key: "outOfTolerance", Val: strconv.FormatBool(outOfTolerance)},
			{Key: "cpuset", Val: cpuset},
		}...)
	}
}

// clearResidualState is used to clean residual pods in local state
func (p *DynamicPolicy) clearResidualState(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec clearResidualState")
	var (
		err     error
		podList []*v1.Pod
	)

	defer func() {
		_ = general.UpdateHealthzStateByError(cpuconsts.ClearResidualState, err)
	}()

	if p.meta.metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	ctx := context.WithValue(context.Background(), metapod.BypassCacheKey, metapod.BypassCacheTrue)
	podList, err = p.meta.metaServer.GetPodList(ctx, native.PodIsActive)
	if err != nil {
		general.Errorf("get pod list failed: %v", err)
		return
	}

	err = p.clearResidualStateAfterPodList(ctx, podList)
	if err != nil {
		general.ErrorS(err, "clear residual state failed")
	}
}

func advisorPostCommitStuckThreshold(conf *coreconfig.Configuration) time.Duration {
	threshold := 2 * cpuSetAdjustmentHandlerTimeout(conf)
	healthWindow := time.Duration(healthCheckTolerationTimes) * stateCheckPeriod
	maxThreshold := healthWindow - stateCheckPeriod
	if threshold <= 0 || threshold > maxThreshold {
		return maxThreshold
	}
	return threshold
}

func advisorPostCommitCleanupWait(conf *coreconfig.Configuration) time.Duration {
	wait := cpuSetAdjustmentHandlerTimeout(conf) / 2
	maxWait := stateCheckPeriod / 4
	if wait <= 0 || wait > maxWait {
		return maxWait
	}
	return wait
}

// clearResidualStateAfterPodList performs at most two cleanup observations
// around an in-flight advisor target. It defers residual mutation while the
// post-commit fence can still advance, then retries against the latest
// canonical revision so cleanup cannot overwrite a committed advisor delta.
func (p *DynamicPolicy) clearResidualStateAfterPodList(
	ctx context.Context,
	podList []*v1.Pod,
) error {
	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	wait := advisorPostCommitCleanupWait(p.config.conf)
	aged := make(map[string]struct{})
	for attempt := 0; attempt < 2; attempt++ {
		p.Lock()
		progress, err := p.clearResidualStateAttempt(ctx, podSet, aged)
		p.Unlock()
		if err != nil {
			return err
		}
		if progress.target == nil {
			return nil
		}
		if attempt == 1 {
			return nil
		}

		timer := time.NewTimer(wait)
		select {
		case <-progress.changed:
		case <-timer.C:
		case <-ctx.Done():
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	return nil
}

// clearResidualStateAttempt observes and ages residuals before consulting the
// post-commit fence. Canonical state mutation remains behind the final writer
// fence and revision CAS.
func (p *DynamicPolicy) clearResidualStateAttempt(
	ctx context.Context,
	podSet sets.String,
	aged map[string]struct{},
) (advisorPostCommitProgress, error) {
	if p.residualHitMap == nil {
		p.residualHitMap = make(map[string]int64)
	}

	residualSet := make(map[string]bool)
	expectedRevision := p.state.GetRevision()
	podEntries := p.state.GetPodEntries()
	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		if !podSet.Has(podUID) {
			residualSet[podUID] = true
			if _, alreadyAged := aged[podUID]; !alreadyAged {
				p.residualHitMap[podUID]++
				aged[podUID] = struct{}{}
			}
			general.Infof("found pod: %s with state but doesn't show up in pod watcher, hit count: %d", podUID, p.residualHitMap[podUID])
		}
	}

	podsToDelete := sets.NewString()
	for podUID, hitCount := range p.residualHitMap {
		if !residualSet[podUID] {
			general.Infof("already found pod: %s in pod watcher or its state is cleared, delete it from residualHitMap", podUID)
			delete(p.residualHitMap, podUID)
			continue
		}

		if time.Duration(hitCount)*stateCheckPeriod >= maxResidualTime {
			podsToDelete.Insert(podUID)
		}
	}

	progress := p.currentAdvisorPostCommitProgress()
	if progress.target != nil {
		sinceProgress := time.Since(progress.lastProgressAt)
		if p.advisorPostCommitProgressStuck(progress) {
			// Release an aged physical-apply fence only when mature residuals
			// need the writer. The target WAL remains the retry owner until all
			// physical side effects converge.
			if podsToDelete.Len() == 0 {
				return progress, fmt.Errorf(
					"advisor post-commit target stuck: revision=%d phase=%s generation=%d target_age=%s since_progress=%s",
					progress.revision, progress.phase, progress.generation,
					time.Since(progress.createdAt), sinceProgress)
			}
			released, err := p.recoverStuckAdvisorPostCommitTargetLocked(ctx, progress.target)
			if err != nil {
				return progress, fmt.Errorf("recover stuck advisor post-commit target: %w", err)
			}
			if !released {
				return p.currentAdvisorPostCommitProgress(), nil
			}
			// The WAL remains durable while the released fence lets this
			// revision-CAS cleanup supersede the blocked canonical state.
		} else {
			general.InfoS("defer residual state cleanup while advisor post-commit target is progressing",
				"revision", progress.revision,
				"phase", progress.phase,
				"generation", progress.generation,
				"targetAge", time.Since(progress.createdAt),
				"sinceProgress", sinceProgress)
			return progress, nil
		}
	}

	if podsToDelete.Len() > 0 {
		// This is the final gate before external RemovePod side effects and the
		// revision-CAS-protected canonical state commit.
		if err := p.ensureCPUStateWriterAllowed(
			expectedRevision, "clearResidualState", nil); err != nil {
			return p.currentAdvisorPostCommitProgress(), nil
		}
		for {
			podUID, found := podsToDelete.PopAny()
			if !found {
				break
			}

			var rErr error
			if p.config.enableCPUAdvisor {
				if p.advisor.advisorClient == nil {
					general.Errorf("remove residual pod: %s in sys advisor failed due to nil cpu advisor client, remain it in state", podUID)
					continue
				}
				_, rErr = p.advisor.advisorClient.RemovePod(ctx, &advisorsvc.RemovePodRequest{
					PodUid: podUID,
				})
			}
			if rErr != nil {
				general.Errorf("remove residual pod: %s in sys advisor failed with error: %v, remain it in state", podUID, rErr)
				continue
			}

			general.Infof("clear residual pod: %s in state", podUID)
			delete(podEntries, podUID)
		}

		updatedMachineState, generateErr := generateMachineStateFromPodEntries(
			p.machine.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
		if generateErr != nil {
			general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", generateErr)
			return advisorPostCommitProgress{}, generateErr
		}

		adjustErr := p.adjustAllocationEntriesAfterDeletionAtRevision(
			podEntries, updatedMachineState, true, expectedRevision)
		if adjustErr != nil {
			general.ErrorS(adjustErr, "adjustAllocationEntries failed")
			return advisorPostCommitProgress{}, adjustErr
		}
	}
	return advisorPostCommitProgress{}, nil
}

func (p *DynamicPolicy) persistPodDeletionAfterAdjustFailure(
	adjustErr error,
	podEntries state.PodEntries,
	updatedMachineState state.NUMANodeMap,
	expectedRevision uint64,
) error {
	if adjustErr == nil {
		return nil
	}

	if !isDefaultShareResidualQuantityGateError(adjustErr) {
		return adjustErr
	}

	general.Warningf("bypass default share residual quantity gate for durable pod deletion: %v", adjustErr)
	_, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: expectedRevision,
		entries:          podEntries,
		baseMachineState: updatedMachineState,
		allowOverlap:     p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		disableDedicated: p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		persist:          true,
		source:           "deletion fallback",
		validate:         p.validatePendingAdvisorPartitionViewWithRampUpExclusion,
	})
	return err
}

func isDefaultShareResidualQuantityGateError(err error) bool {
	if stderrors.Is(err, ErrDefaultShareResidualQuantityMismatch) {
		return true
	}
	var quantityErr *DefaultShareResidualQuantityError
	return stderrors.As(err, &quantityErr)
}

// syncCPUIdle is used to set cpu idle for reclaimed cores
func (p *DynamicPolicy) syncCPUIdle(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec syncCPUIdle")
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncCPUIdle, err)
	}()

	if !cgroupcm.IsCPUIdleSupported() {
		general.Warningf("cpu idle isn't unsupported, skip syncing")
		return
	}

	existingPaths := cgroupcm.GetExistingRelativeCgroupPaths(p.config.reclaimRelativeRootCgroupPaths...)
	err = cgroupcmutils.ApplyCPUWithRelativePaths(existingPaths, &cgroupcm.CPUData{CpuIdlePtr: &p.config.enableCPUIdle})
	if err != nil {
		general.Errorf("ApplyCPUWithRelativePaths in %v with enableCPUIdle: %v failed with error: %v",
			existingPaths, p.config.enableCPUIdle, err)
	}

	// sync numa binding reclaim cgroup
	for numaID, paths := range p.config.numaBindingReclaimRelativeRootCgroupPaths {
		existingNUMAPaths := cgroupcm.GetExistingRelativeCgroupPaths(paths...)
		if len(existingNUMAPaths) == 0 {
			continue
		}
		if err := cgroupcmutils.ApplyCPUWithRelativePaths(existingNUMAPaths, &cgroupcm.CPUData{CpuIdlePtr: &p.config.enableCPUIdle}); err != nil {
			general.Errorf("ApplyCPUWithRelativePaths in %v (numa %d) with enableCPUIdle: %v failed with error: %v",
				existingNUMAPaths, numaID, p.config.enableCPUIdle, err)
		}
	}
}

// syncCPUBurst is used to periodically set the cpu burst for every pod
func (p *DynamicPolicy) syncCPUBurst(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec syncCPUBurst")

	var err error

	defer func() {
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncCPUBurst, err)
	}()

	cpuBurstManager := cpuburst.GetManager(p.meta.metaServer)
	err = cpuBurstManager.UpdateCPUBurst(p.config.conf, p.config.dynamicConfig)
}

// syncContainerCPUIdle is used to periodically set cpu idle for containers selected by pod annotations.
func (p *DynamicPolicy) syncContainerCPUIdle(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec syncContainerCPUIdle")

	var err error

	defer func() {
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncContainerCPUIdle, err)
	}()

	cpuIdleManager := cpuidle.GetManager(p.meta.metaServer)
	err = cpuIdleManager.UpdateContainerCPUIdle(p.config.conf)
}

func (p *DynamicPolicy) syncSystemExclusivePool(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("[SystemExclusivePool] exec sync")

	var err error

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			general.Errorf("[SystemExclusivePool] failed with error: %v", err)
		}
		general.Infof("[SystemExclusivePool] completed successfully")
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncSystemExclusivePool, err)
	}()

	if !p.config.conf.EnableSystemExclusivePool {
		general.Infof("[SystemExclusivePool] disabled")
		return
	}

	err = p.reconcileSystemExclusivePools()
}

func (p *DynamicPolicy) reconcileSystemExclusivePools() error {
	currentPools := p.listCurrentSystemExclusivePools()
	expectedPools := p.getExpectedSystemExclusivePools()
	p.emitSystemExclusivePoolSizes(currentPools, expectedPools)

	toCreate, toUpdate, toDelete := p.calculateSystemExclusivePoolChanges(currentPools, expectedPools)

	if len(toCreate) == 0 && len(toUpdate) == 0 && toDelete.Len() == 0 {
		return nil
	}

	if err := p.applySystemExclusivePoolChanges(toCreate, toUpdate, toDelete); err != nil {
		return fmt.Errorf("apply system exclusive pool changes failed: %v", err)
	}

	return nil
}

func (p *DynamicPolicy) listCurrentSystemExclusivePools() map[string]*state.AllocationInfo {
	currentPools := make(map[string]*state.AllocationInfo)
	for name, entry := range p.state.GetPodEntries() {
		if !entry.IsPoolEntry() || !commonstate.IsSystemPool(name) {
			continue
		}
		currentPools[name] = entry[commonstate.FakedContainerName].Clone()
	}
	return currentPools
}

func (p *DynamicPolicy) getExpectedSystemExclusivePools() map[string]int {
	dynamicConfig := p.config.dynamicConfig.GetDynamicConfiguration()
	configuredPools := dynamicConfig.SystemExclusivePool

	expectedPools := make(map[string]int)
	for name, size := range configuredPools {
		if size <= 0 {
			general.Warningf("[SystemExclusivePool] system exclusive pool %s size %d is invalid, skip", name, size)
			continue
		}
		expectedPools[commonstate.GetSystemPoolName(name)] = size
	}

	return expectedPools
}

func (p *DynamicPolicy) emitSystemExclusivePoolSizes(currentPools map[string]*state.AllocationInfo, expectedPools map[string]int) {
	if p.emitter.emitter == nil {
		return
	}

	poolNames := sets.NewString()
	for poolName := range expectedPools {
		poolNames.Insert(poolName)
	}
	for poolName := range currentPools {
		poolNames.Insert(poolName)
	}

	for _, poolName := range poolNames.List() {
		poolSize, poolStatus := getSystemExclusivePoolMetricValueAndStatus(currentPools, expectedPools, poolName)
		_ = p.emitter.emitter.StoreInt64(util.MetricNameSystemExclusivePoolSize, poolSize, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "pool_name", Val: poolName},
			metrics.MetricTag{Key: "status", Val: poolStatus})
	}
}

func getSystemExclusivePoolMetricValueAndStatus(currentPools map[string]*state.AllocationInfo, expectedPools map[string]int, poolName string) (int64, string) {
	expectedSize, expectedExists := expectedPools[poolName]
	allocationInfo, actualExists := currentPools[poolName]
	actualSize := 0
	if actualExists {
		actualSize = allocationInfo.AllocationResult.Size()
	}

	switch {
	case expectedExists && actualExists && expectedSize == actualSize:
		return int64(actualSize), "ready"
	case expectedExists && !actualExists:
		return 0, "absent"
	case !expectedExists && actualExists:
		return int64(actualSize), "stale"
	default:
		return int64(actualSize), "updating"
	}
}

// calculateSystemExclusivePoolChanges calculates the system exclusive pool changes according to the current pools and expected pools
// it returns the pools to change:
//
//	toCreate: the pools to create, the key is the pool name, the value is the size to allocate
//	toUpdate: the pools to update, the key is the pool name, the value is the size to shrink or expand depending on it is positive or negative
//	toDelete: the list of pool names to delete
func (p *DynamicPolicy) calculateSystemExclusivePoolChanges(
	currentPools map[string]*state.AllocationInfo,
	expectedPools map[string]int,
) (map[string]int, map[string]int, sets.String) {
	shrinkRatio := defaultSystemExclusivePoolShrinkRatio
	shrinkMin := defaultSystemExclusivePoolShrinkMin
	shrinkMax := defaultSystemExclusivePoolShrinkMax
	dynamicConfig := p.config.dynamicConfig.GetDynamicConfiguration()
	if dynamicConfig.SystemExclusivePoolShrinkRatio != nil {
		shrinkRatio = *dynamicConfig.SystemExclusivePoolShrinkRatio
	}
	if shrinkRatio <= 0.0 || shrinkRatio >= 1.0 {
		general.Warningf("[SystemExclusivePool] invalid shrinkRatio(%f) from dynamic config, "+
			"fallback to default value: shrinkRatio(%f)", shrinkRatio, defaultSystemExclusivePoolShrinkRatio)
		shrinkRatio = defaultSystemExclusivePoolShrinkRatio
	}
	if dynamicConfig.SystemExclusivePoolShrinkMin != nil && *dynamicConfig.SystemExclusivePoolShrinkMin > 0 {
		shrinkMin = int(*dynamicConfig.SystemExclusivePoolShrinkMin)
	}
	if dynamicConfig.SystemExclusivePoolShrinkMax != nil && *dynamicConfig.SystemExclusivePoolShrinkMax > 0 {
		shrinkMax = int(*dynamicConfig.SystemExclusivePoolShrinkMax)
	}
	if shrinkMin >= shrinkMax {
		general.Warningf("[SystemExclusivePool] invalid shrinkMin(%d) or shrinkMax(%d) from dynamic config, "+
			"fallback to default value: shrinkMin(%d) shrinkMax(%d)", shrinkMin, shrinkMax, defaultSystemExclusivePoolShrinkMin, defaultSystemExclusivePoolShrinkMax)
		shrinkMin = defaultSystemExclusivePoolShrinkMin
		shrinkMax = defaultSystemExclusivePoolShrinkMax
	}

	var (
		toCreate = make(map[string]int)
		toUpdate = make(map[string]int)
		toDelete = sets.NewString()
	)

	for name, allocationInfo := range currentPools {
		expectedSize, exists := expectedPools[name]
		if !exists {
			toDelete.Insert(name)
			continue
		}

		currentSize := allocationInfo.AllocationResult.Size()
		delta := expectedSize - currentSize
		if delta == 0 {
			continue
		}

		if delta > 0 {
			toUpdate[name] = delta
		} else {
			// make sure shrinkSize is even so that we can shrink evenly
			shrinkSize := int(math.Ceil(float64(currentSize) * shrinkRatio))
			if shrinkSize < shrinkMin {
				shrinkSize = shrinkMin
			}
			if shrinkSize%2 != 0 {
				shrinkSize += 1
			}
			if shrinkSize > shrinkMax {
				shrinkSize = shrinkMax
			}
			if shrinkSize%2 != 0 {
				shrinkSize -= 1
			}
			if shrinkSize > int(math.Abs(float64(delta))) {
				shrinkSize = int(math.Abs(float64(delta)))
			}
			toUpdate[name] = -shrinkSize
		}
	}

	// only create pool used by pod
	systemPoolsWithPod := sets.NewString()
	for podUID, entry := range p.state.GetPodEntries() {
		if entry.IsPoolEntry() {
			continue
		}
		for containerName, allocationInfo := range entry {
			if allocationInfo == nil {
				general.Warningf("[SystemExclusivePool] pod %s container %s allocation info is nil during pool change calculation, skip it", podUID, containerName)
				continue
			}
			if !allocationInfo.CheckSystem() {
				continue
			}
			systemPoolsWithPod.Insert(allocationInfo.GetOwnerPoolName())
		}
	}
	for poolName, size := range expectedPools {
		if _, exists := currentPools[poolName]; !exists && systemPoolsWithPod.Has(poolName) {
			toCreate[poolName] = size
		}
	}

	return toCreate, toUpdate, toDelete
}

func (p *DynamicPolicy) applySystemExclusivePoolChanges(toCreate, toUpdate map[string]int, toDelete sets.String) error {
	revision := p.state.GetRevision()
	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()
	allowOverlap := p.state.GetAllowSharedCoresOverlapReclaimedCores()
	disableDedicatedOverlap := p.state.GetDisableDedicatedCoresOverlapReclaimedCores()
	if currentRevision := p.state.GetRevision(); currentRevision != revision {
		return fmt.Errorf("%w while snapshotting system exclusive pool state: expected=%d actual=%d",
			state.ErrStaleStateRevision, revision, currentRevision)
	}
	candidateState := state.NewTransientState(p.machine.machineInfo.CPUTopology)
	if err := candidateState.CommitAdvisorState(
		podEntries, machineState, allowOverlap, disableDedicatedOverlap, false); err != nil {
		return fmt.Errorf("initialize system exclusive pool candidate state: %w", err)
	}

	availableCPUs := candidateState.GetMachineState().GetFilteredAvailableCPUSet(p.config.reservedCPUs,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicated),
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicatedNUMABindingNUMAExclusive))
	notAllocatablePoolsCPUs := state.GetUnitedPoolsCPUs(
		candidateState.GetPodEntries(), state.IsForbiddenPool, commonstate.IsSystemPool)
	availableCPUs = availableCPUs.Difference(notAllocatablePoolsCPUs)

	availableCPUs, err := p.deleteSystemExclusivePoolFromState(candidateState, toDelete, availableCPUs)
	if err != nil {
		return fmt.Errorf("delete system exclusive pool failed with error: %v", err)
	}

	availableCPUs, err = p.updateSystemExclusivePoolInState(candidateState, toUpdate, availableCPUs)
	if err != nil {
		return fmt.Errorf("shrink system exclusive pool failed with error: %v", err)
	}

	_, err = p.createSystemExclusivePoolInState(candidateState, toCreate, availableCPUs)
	if err != nil {
		return fmt.Errorf("create system exclusive pool failed with error: %v", err)
	}

	if err := p.adjustSystemCoresPodAllocationInState(candidateState); err != nil {
		return fmt.Errorf("adjust system exclusive pool failed with error: %v", err)
	}

	candidateEntries := candidateState.GetPodEntries()
	updatedMachineState, err := generateMachineStateFromPodEntries(
		p.machine.machineInfo.CPUTopology, candidateEntries, candidateState.GetMachineState())
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed: %v", err)
	}
	if err := p.state.CommitAdvisorStateIfRevision(
		revision,
		candidateEntries,
		updatedMachineState,
		allowOverlap,
		disableDedicatedOverlap,
		true,
	); err != nil {
		return fmt.Errorf("commit system exclusive pool state: %w", err)
	}

	return nil
}

func (p *DynamicPolicy) deleteSystemExclusivePoolFromState(
	stateStore state.State,
	toDelete sets.String,
	availableCPUs machine.CPUSet,
) (machine.CPUSet, error) {
	for _, name := range toDelete.List() {
		allocationInfo := stateStore.GetAllocationInfo(name, commonstate.FakedContainerName)
		if allocationInfo == nil {
			general.Warningf("[SystemExclusivePool] get nil allocationInfo for pool %s, skip.", name)
			continue
		}

		general.Infof("[SystemExclusivePool] delete pool %s", name)
		if err := stateStore.Delete(name, commonstate.FakedContainerName, false); err != nil {
			return machine.CPUSet{}, fmt.Errorf("delete system exclusive pool %s state: %w", name, err)
		}
		availableCPUs = availableCPUs.Union(allocationInfo.AllocationResult)
	}

	return availableCPUs, nil
}

func (p *DynamicPolicy) updateSystemExclusivePool(toShrink map[string]int, availableCPUs machine.CPUSet) (machine.CPUSet, error) {
	return p.updateSystemExclusivePoolInState(p.state, toShrink, availableCPUs)
}

func (p *DynamicPolicy) updateSystemExclusivePoolInState(
	stateStore state.State,
	toShrink map[string]int,
	availableCPUs machine.CPUSet,
) (machine.CPUSet, error) {
	sortedPool := make([]string, 0, len(toShrink))
	for poolName := range toShrink {
		sortedPool = append(sortedPool, poolName)
	}
	sort.Slice(sortedPool, func(i, j int) bool {
		return toShrink[sortedPool[i]] < toShrink[sortedPool[j]]
	})
	for _, name := range sortedPool {
		delta := toShrink[name]
		allocationInfo := stateStore.GetAllocationInfo(name, commonstate.FakedContainerName)
		if allocationInfo == nil {
			general.Warningf("[SystemExclusivePool] get nil allocationInfo for pool %s, skip.", name)
			continue
		}

		var allocationResult machine.CPUSet
		if delta > 0 {
			deltaCPUs, _, err := calculator.TakeByNUMABalance(p.machine.machineInfo, availableCPUs, delta)
			if err != nil {
				return machine.CPUSet{}, fmt.Errorf("take HT by NUMABalace failed for pool %s with error: %v", name, err)
			}
			allocationResult = allocationInfo.AllocationResult.Union(deltaCPUs)
			availableCPUs = availableCPUs.Difference(deltaCPUs)
		} else {
			deltaCPUs, _, err := calculator.TakeByNUMABalanceReversely(p.machine.machineInfo, allocationInfo.AllocationResult, -delta)
			if err != nil {
				return machine.CPUSet{}, fmt.Errorf("take HT by NUMABalace failed for pool %s with error: %v", name, err)
			}
			allocationResult = allocationInfo.AllocationResult.Difference(deltaCPUs)
			availableCPUs = availableCPUs.Union(deltaCPUs)
		}

		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, allocationResult)
		if err != nil {
			return machine.CPUSet{}, fmt.Errorf("failed to get numa aware assignments for pool %s: %v", name, err)
		}

		general.Infof("[SystemExclusivePool] update pool %s, delta: %d, origin cpuset: %s, target cpuset: %s", name,
			delta, allocationInfo.AllocationResult, allocationResult)

		allocationInfo.AllocationResult = allocationResult
		allocationInfo.OriginalAllocationResult = allocationResult
		allocationInfo.TopologyAwareAssignments = topologyAwareAssignments
		allocationInfo.OriginalTopologyAwareAssignments = topologyAwareAssignments
		if err := stateStore.SetAllocationInfo(
			name, commonstate.FakedContainerName, allocationInfo, false); err != nil {
			return machine.CPUSet{}, fmt.Errorf("update system exclusive pool %s state: %w", name, err)
		}
	}

	return availableCPUs, nil
}

func (p *DynamicPolicy) createSystemExclusivePool(toCreate map[string]int, availableCPUs machine.CPUSet) (machine.CPUSet, error) {
	return p.createSystemExclusivePoolInState(p.state, toCreate, availableCPUs)
}

func (p *DynamicPolicy) createSystemExclusivePoolInState(
	stateStore state.State,
	toCreate map[string]int,
	availableCPUs machine.CPUSet,
) (machine.CPUSet, error) {
	for name, size := range toCreate {
		allocationResult, _, err := calculator.TakeByNUMABalance(p.machine.machineInfo, availableCPUs, size)
		if err != nil {
			return machine.CPUSet{}, fmt.Errorf("failed to allocate CPUs for system exclusive pool %s: %v", name, err)
		}

		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, allocationResult)
		if err != nil {
			return machine.CPUSet{}, fmt.Errorf("failed to get numa aware assignments for system exclusive pool %s: %v", name, err)
		}

		poolAllocationInfo := &state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(name),
			AllocationResult:                 allocationResult,
			OriginalAllocationResult:         allocationResult,
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: topologyAwareAssignments,
		}

		general.Infof("[SystemExclusivePool] creating pool %s with size %d, cpuset: %s", name, size, allocationResult)

		if err := stateStore.SetAllocationInfo(
			name, commonstate.FakedContainerName, poolAllocationInfo, false); err != nil {
			return machine.CPUSet{}, fmt.Errorf("create system exclusive pool %s state: %w", name, err)
		}
		availableCPUs = availableCPUs.Difference(allocationResult)
	}

	return availableCPUs, nil
}

func (p *DynamicPolicy) adjustSystemCoresPodAllocation() error {
	return p.adjustSystemCoresPodAllocationInState(p.state)
}

func (p *DynamicPolicy) adjustSystemCoresPodAllocationInState(stateStore state.State) error {
	defaultSystemCoresCPUSet := p.machine.machineInfo.CPUDetails.CPUs()
	defaultSystemCoresTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, defaultSystemCoresCPUSet)
	if err != nil {
		return fmt.Errorf("failed to get numa aware assignments for default system cores: %v", err)
	}

	for podUID, entry := range stateStore.GetPodEntries() {
		if entry.IsPoolEntry() {
			continue
		}

		for containerName, allocationInfo := range entry {
			if allocationInfo == nil {
				general.Warningf("[SystemExclusivePool] pod %s container %s allocation info is nil during system cores pod adjustment, skip it", podUID, containerName)
				continue
			}
			if !allocationInfo.CheckSystem() {
				continue
			}

			poolName := allocationInfo.GetOwnerPoolName()
			if poolName == commonstate.EmptyOwnerPoolName {
				continue
			}

			poolAllocationInfo := stateStore.GetAllocationInfo(poolName, commonstate.FakedContainerName)
			if poolAllocationInfo == nil {
				// pool not found, use default system cores
				if !allocationInfo.AllocationResult.Equals(defaultSystemCoresCPUSet) {
					general.Infof("[SystemExclusivePool] no configured pool for pod %s/%s container %s, using default cpuset: %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, containerName, defaultSystemCoresCPUSet)

					allocationInfo.AllocationResult = defaultSystemCoresCPUSet
					allocationInfo.OriginalAllocationResult = defaultSystemCoresCPUSet
					allocationInfo.TopologyAwareAssignments = defaultSystemCoresTopologyAwareAssignments
					allocationInfo.OriginalTopologyAwareAssignments = defaultSystemCoresTopologyAwareAssignments
					if err := stateStore.SetAllocationInfo(podUID, containerName, allocationInfo, false); err != nil {
						return fmt.Errorf("reset system pod %s/%s allocation: %w", podUID, containerName, err)
					}
				}
				continue
			}

			if allocationInfo.AllocationResult.Equals(poolAllocationInfo.AllocationResult) {
				continue
			}

			general.Infof("[SystemExclusivePool] adjust cpuset of pod %s/%s container %s, according to pool %s, origin cpuset: %s, target cpuset: %s",
				allocationInfo.PodNamespace, allocationInfo.PodName, containerName, poolName, allocationInfo.AllocationResult, poolAllocationInfo.AllocationResult)

			allocationInfo.AllocationResult = poolAllocationInfo.AllocationResult.Clone()
			allocationInfo.OriginalAllocationResult = poolAllocationInfo.OriginalAllocationResult.Clone()
			allocationInfo.TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolAllocationInfo.TopologyAwareAssignments)
			allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolAllocationInfo.OriginalTopologyAwareAssignments)
			if err := stateStore.SetAllocationInfo(podUID, containerName, allocationInfo, false); err != nil {
				return fmt.Errorf("update system pod %s/%s allocation: %w", podUID, containerName, err)
			}
		}
	}

	return nil
}

func (p *DynamicPolicy) syncCPUWeight(_ *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec syncCPUWeight")

	var err error
	defer func() {
		if err != nil {
			general.ErrorS(err, "syncCPUWeight failed")
		} else {
			general.Infof("syncCPUWeight succeed")
		}
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncCPUWeight, err)
	}()

	cpuWeightManager := cpuweight.GetManager(p.meta.metaServer, p.emitter.emitter)
	err = cpuWeightManager.UpdateCPUWeight(p.config.dynamicConfig)
}
