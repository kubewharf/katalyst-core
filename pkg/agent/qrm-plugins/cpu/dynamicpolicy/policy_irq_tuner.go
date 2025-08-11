package dynamicpolicy

import (
	"context"
	"fmt"
	"math"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner"
	irqutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func (p *DynamicPolicy) SetIRQTuner(irqTuner irqtuner.Tuner) {
	p.irqTuner = irqTuner
}

// ListContainers retrieves the container info of all running containers.
func (p *DynamicPolicy) ListContainers() ([]irqtuner.ContainerInfo, error) {
	var cis []irqtuner.ContainerInfo

	// 1. get container info from pod entries
	for podUID, entry := range p.state.GetPodEntries() {
		if entry.IsPoolEntry() {
			continue
		}

		infos, err := p.getPodContainerInfos(podUID, entry)
		if err != nil {
			general.Warningf("get pod %v container infos failed: %v", podUID, err)
			continue
		}

		cis = append(cis, infos...)
	}

	return cis, nil
}

func (p *DynamicPolicy) getPodContainerInfos(podUID string, entry state.ContainerEntries) ([]irqtuner.ContainerInfo, error) {
	cis := make([]irqtuner.ContainerInfo, 0)

	if entry.IsPoolEntry() {
		return cis, fmt.Errorf("this is a pool entry")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// get the pod from meta server
	pod, err := p.metaServer.PodFetcher.GetPod(ctx, podUID)
	if err != nil || pod == nil {
		return cis, fmt.Errorf("pod fetcher cannot get pod %s, err:%v", podUID, err)
	}

	// get the runtime class from pod spec
	var runtimeClassName string
	if pod.Spec.RuntimeClassName != nil {
		runtimeClassName = *pod.Spec.RuntimeClassName
	}

	// get the container status
	containerStatus := make(map[string]v1.ContainerStatus)
	for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		containerStatus[cs.Name] = cs
	}

	// get the container info from the current persistent entry of the node
	for containerName, allocationInfo := range entry {
		if allocationInfo == nil {
			general.Warningf("container %s allocation info is nil, skip it", containerName)
			continue
		}

		// get the container ID
		containerID, err := p.metaServer.PodFetcher.GetContainerID(podUID, containerName)
		if err != nil {
			general.Warningf("unable to get container id from pod %s/%s: %v", podUID, containerName, err)
			continue
		}

		// get the cgroup path
		cgroupPath, err := common.GetContainerRelativeCgroupPath(podUID, containerID)
		if err != nil {
			general.Warningf("unable to get container cgroup path from pod %s/%s: %v", podUID, containerName, err)
			continue
		}

		// get the started time
		var startedAt metav1.Time
		if cs, exist := containerStatus[containerName]; exist && cs.State.Running != nil {
			startedAt = cs.State.Running.StartedAt
		} else {
			general.Infof("container %s not running, skip it", containerName)
			continue
		}

		ci := irqtuner.ContainerInfo{
			AllocationMeta:   allocationInfo.AllocationMeta.Clone(),
			ContainerID:      containerID,
			CgroupPath:       cgroupPath,
			RuntimeClassName: runtimeClassName,
			ActualCPUSet:     allocationInfo.TopologyAwareAssignments,
			StartedAt:        startedAt,
		}
		// inject pod complete anno
		ci.Annotations = general.MergeMap(pod.Annotations, ci.Annotations)

		cis = append(cis, ci)
	}

	return cis, nil
}

// GetIRQForbiddenCores retrieves the cpu set of cores that are forbidden for irq binding.
func (p *DynamicPolicy) GetIRQForbiddenCores() (machine.CPUSet, error) {
	forbiddenCores := machine.NewCPUSet()

	// get irq forbidden cores from cpu plugin checkpoint
	forbiddenCores.Union(p.reservedCPUs)
	// TODO: add katabm cores

	general.Infof("get the irq forbidden cores %v", forbiddenCores)
	return forbiddenCores, nil
}

func (p *DynamicPolicy) GetStepExpandableCPUsMax() int {
	availableTotalCPUSetSize := p.state.GetMachineState().GetAvailableCPUSet(p.reservedCPUs).Size()
	res := int(math.Ceil(irqutil.DefaultIRQExclusiveMaxStepExpansionRate * float64(availableTotalCPUSetSize)))

	return res
}

// GetExclusiveIRQCPUSet retrieves the cpu set of cores that are exclusive for irq binding.
func (p *DynamicPolicy) GetExclusiveIRQCPUSet() (machine.CPUSet, error) {
	currentIRQCPUSet := machine.NewCPUSet()
	podEntries := p.state.GetPodEntries()
	if containerEntry, ok := podEntries[commonstate.PoolNameInterrupt]; ok {
		if allocateInfo, ok := containerEntry[commonstate.FakedContainerName]; ok && allocateInfo != nil {
			currentIRQCPUSet = allocateInfo.AllocationResult.Clone()
		}
	}

	general.Infof("get the current irq exclusive cpu set: %v", currentIRQCPUSet)
	return currentIRQCPUSet, nil
}

// SetExclusiveIRQCPUSet sets the exclusive cpu set for Interrupt.
func (p *DynamicPolicy) SetExclusiveIRQCPUSet(irqCPUSet machine.CPUSet) error {
	general.Infof("set the current irq exclusive cpu set: %v", irqCPUSet)

	forbidden, err := p.GetIRQForbiddenCores()
	if err != nil {
		general.Errorf("get irq forbidden cores failed, err:%v", err)
		return err
	}

	// 1. check cpuSet nums（max）
	irqCPUSetSize := irqCPUSet.Size()
	availableTotalCPUSetSize := p.state.GetMachineState().GetAvailableCPUSet(p.reservedCPUs).Size()
	maxExpandableSize := int(math.Ceil(float64(availableTotalCPUSetSize) * irqutil.DefaultIRQExclusiveMaxExpansionRate))
	if irqCPUSetSize >= maxExpandableSize {
		general.Errorf("the specified number of cpusets %v exceeds the max amount %v", irqCPUSetSize, maxExpandableSize)
		return irqutil.ExceededMaxExpandableCapacityErr
	}

	// 2. measuring the rate at which the irq exclusive core expansion
	var currentIrqCPUSet machine.CPUSet
	podEntries := p.state.GetPodEntries()
	if containerEntry, ok := podEntries[commonstate.PoolNameInterrupt]; ok {
		if allocateInfo, ok := containerEntry[commonstate.FakedContainerName]; ok && allocateInfo != nil {
			currentIrqCPUSet = allocateInfo.AllocationResult
		}
	}

	currentIrqCPUSetSize := currentIrqCPUSet.Size()
	expandSize := irqCPUSetSize - currentIrqCPUSetSize
	maxStepExpandableSize := p.GetStepExpandableCPUsMax()
	// If the number of CPUs that interrupt exclusive cores is increased exceeds the maximum number
	// of CPUs that can be adjusted at a time, an error will be returned.
	if expandSize > 0 && expandSize > maxStepExpandableSize {
		general.Errorf("the specified number of cpusets %v exceeds the max amount %v", irqCPUSetSize, maxStepExpandableSize)
		return irqutil.ExceededMaxStepExpandableCapacityErr
	}

	// 3. check cpuSet is intersection of irq forbidden cores
	if irqCPUSet.Intersection(forbidden).Size() != 0 {
		general.Errorf("the cpuset[%v] passed in contains the cpu that is forbidden[%v] to bind", irqCPUSet, forbidden)
		return irqutil.ContainForbiddenCPUErr
	}

	// 4. update cpu plugin checkpoint
	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, irqCPUSet)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for entry: %s, entry cpuset: %s, error: %v",
			commonstate.PoolNameInterrupt, irqCPUSet.String(), err)
	}

	ai := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        commonstate.PoolNameInterrupt,
			OwnerPoolName: commonstate.PoolNameInterrupt,
		},
		AllocationResult:                 irqCPUSet.Clone(),
		OriginalAllocationResult:         irqCPUSet.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
	}

	newPodEntries := p.state.GetPodEntries()
	if _, ok := newPodEntries[commonstate.PoolNameInterrupt]; !ok {
		newPodEntries[commonstate.PoolNameInterrupt] = state.ContainerEntries{}
	}
	if _, ok := newPodEntries[commonstate.PoolNameInterrupt][commonstate.FakedContainerName]; !ok {
		newPodEntries[commonstate.PoolNameInterrupt][commonstate.FakedContainerName] = &state.AllocationInfo{}
	}
	newPodEntries[commonstate.PoolNameInterrupt][commonstate.FakedContainerName] = ai

	machineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, newPodEntries)
	if err != nil {
		return fmt.Errorf("calculate machineState by newPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(newPodEntries, true)
	p.state.SetMachineState(machineState, true)

	_ = p.emitter.StoreInt64(util.MetricNameSetExclusiveIRQCPUSize, int64(irqCPUSetSize), metrics.MetricTypeNameRaw)
	general.Infof("persistent irq exclusive cpu set %v successful", irqCPUSet.String())

	return nil
}
