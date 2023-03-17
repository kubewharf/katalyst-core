package headroompolicy

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
)

type PolicyCanonical struct {
	*PolicyBase
	IsInitialized  bool
	CPURequirement int64
}

func NewPolicyCanonical(metaCache *metacache.MetaCache, metaServer *metaserver.MetaServer) HeadroomPolicy {
	return &PolicyCanonical{
		PolicyBase:    NewPolicyBase(metaCache, metaServer),
		IsInitialized: false,
	}
}

func (p *PolicyCanonical) Update() error {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	var (
		cpuEstimation float64 = 0
		containerCnt  float64 = 0
	)

	for podUID, v := range p.ContainerSet {
		for containerName := range v {
			ci, ok := p.MetaCache.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				return fmt.Errorf("[qosaware-cpu-canonical] illegal container info of %v/%v", podUID, containerName)
			}

			containerEstimation, err := helper.EstimateContainerResourceUsage(ci, v1.ResourceCPU, p.MetaCache)
			if err != nil {
				return fmt.Errorf("[headroom-cpu-canonical] pod %v container %v estimation err %v", ci.PodName, containerName, err)
			}

			cpuEstimation += containerEstimation
			containerCnt += 1
		}
	}
	klog.Infof("[headroom-cpu-canonical] cpu requirement estimation: %.2f, #container %v", cpuEstimation, containerCnt)

	p.CPURequirement = int64(cpuEstimation)
	p.IsInitialized = true
	return nil
}

func (p *PolicyCanonical) GetHeadroom() (resource.Quantity, error) {
	p.Mutex.RLock()
	defer p.Mutex.RUnlock()
	q := resource.Quantity{}
	if !p.IsInitialized {
		return q, fmt.Errorf("[headroom-cpu-canonical] policy is not initialized")
	}

	return *resource.NewQuantity(p.CPULimit-p.CPURequirement-p.CPUReserved, resource.DecimalSI), nil
}
