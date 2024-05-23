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

package isolation

import (
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu"
	metric_consts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type containerIsolationState struct {
	lockedInHits           int
	lockedOutFirstObserved *time.Time
}

type poolIsolationStates struct {
	// totalResources records total cpu in this pool
	// podResources records cpu for this pod-uid
	totalResources float64
	podResources   map[string]float64

	maxResources     float64
	maxResourceRatio float32
	maxPods          int
	maxPodRatio      float32

	// isolatedPods records pod-uid that already isolated
	// isolatedResources records total already-isolated cpu in this pool
	isolatedPods      sets.String
	isolatedResources float64
}

// LoadIsolator decides isolation states based on cpu-load for containers
type LoadIsolator struct {
	conf *cpu.CPUIsolationConfiguration

	emitter    metrics.MetricEmitter
	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer

	// map from pod/container pair to containerIsolationState
	states sync.Map

	configTranslator *general.CommonSuffixTranslator
}

func NewLoadIsolator(conf *config.Configuration, _ interface{}, emitter metrics.MetricEmitter,
	metaCache metacache.MetaReader, metaServer *metaserver.MetaServer,
) Isolator {
	return &LoadIsolator{
		conf: conf.CPUIsolationConfiguration,

		emitter:    emitter,
		metaReader: metaCache,
		metaServer: metaServer,

		configTranslator: general.NewCommonSuffixTranslator("-NUMA"),
	}
}

func (l *LoadIsolator) GetIsolatedPods() []string {
	if l.conf.IsolationDisabled {
		return []string{}
	}

	isolationResources := l.initIsolationStates()
	for k, v := range isolationResources {
		general.Infof("initialized %v resource: %v", k, *v)
	}

	// walk through each container to judge whether it should be isolated
	uidSets := sets.NewString()
	existed := sets.NewString()
	for _, ci := range l.getSortedContainerInfo() {
		// if isolation is disabled from the certain pool, mark as none-isolated and trigger container clear
		if l.conf.IsolationDisabledPools.Has(l.configTranslator.Translate(ci.OriginOwnerPoolName)) {
			continue
		}

		existed.Insert(containerMeta(ci))
		if l.checkContainerIsolated(ci, isolationResources) {
			general.Infof("add container %s from pod %s/%s to isolation", ci.ContainerName, ci.PodNamespace, ci.PodName)
			uidSets.Insert(ci.PodUID)
		}
	}

	// clear in-memory cached isolation states if the corresponding container exited
	l.states.Range(func(key, _ interface{}) bool {
		if !existed.Has(key.(string)) {
			l.states.Delete(key)
		}
		return true
	})

	return uidSets.List()
}

// checkContainerIsolated returns true if current container for isolation
func (l *LoadIsolator) checkContainerIsolated(info *types.ContainerInfo, isolationResources map[string]*poolIsolationStates) (isolated bool) {
	// if this pod has already been locked-in, just return
	if isolationResources[info.OriginOwnerPoolName] != nil && isolationResources[info.OriginOwnerPoolName].isolatedPods.Has(info.PodUID) {
		return false
	}

	// record container resources if we finally define this container as isolated
	defer func() {
		if isolated {
			isolationResources[info.OriginOwnerPoolName].isolatedPods.Insert(info.PodUID)
			isolationResources[info.OriginOwnerPoolName].isolatedResources += isolationResources[info.OriginOwnerPoolName].podResources[info.PodUID]
		}
	}()

	if !checkTargetContainer(info) {
		return false
	} else if !l.checkIsolationPoolThreshold(info, isolationResources) {
		return false
	}

	return l.checkContainerLoad(info)
}

// checkContainerLoad returns true if the load reaches target and last for pre-defined period
func (l *LoadIsolator) checkContainerLoad(info *types.ContainerInfo) bool {
	m, err := l.metaServer.GetContainerMetric(info.PodUID, info.ContainerName, metric_consts.MetricCPUNrRunnableContainer)
	if err != nil {
		// if we failed to get the latest load, keep the isolation states as it is
		general.Errorf("get load for pod %v container %v err: %v", info.PodName, info.ContainerName, err)
		return info.Isolated
	}

	general.Infof("pod %v container %v current load %v", info.PodName, info.ContainerName, m.Value)
	state := l.getIsolationState(info)

	r := getMaxContainerResource(info)
	loadBeyondTarget := m.Value > r*float64(l.conf.IsolationCPURatio) || m.Value > r+float64(l.conf.IsolationCPUSize)
	if loadBeyondTarget {
		// reset lock-out observed and add up lock-in hits
		if state.lockedInHits < l.conf.IsolationLockInThreshold {
			state.lockedInHits++
		}
		state.lockedOutFirstObserved = nil

		general.Infof("pod %v container %v exceeds load", info.PodName, info.ContainerName)
		return info.Isolated || state.lockedInHits >= l.conf.IsolationLockInThreshold
	} else {
		// reset lock-in hits and set lock-out observed (if needed)
		now := time.Now()
		state.lockedInHits = 0
		if state.lockedOutFirstObserved == nil {
			state.lockedOutFirstObserved = &now
		}

		return info.Isolated && state.lockedOutFirstObserved.Add(time.Second*time.Duration(l.conf.IsolationLockOutPeriodSecs)).After(now)
	}
}

// checkIsolationPoolThreshold returns true if this container can be isolated
// aspect of the limitation of total isolated containers in this pool
func (l *LoadIsolator) checkIsolationPoolThreshold(info *types.ContainerInfo, isolationResources map[string]*poolIsolationStates) bool {
	poolResource, ok := isolationResources[info.OriginOwnerPoolName]
	if !ok {
		general.Warningf("pod %v container %v doesn't has pool resource for %s", info.PodName, info.ContainerName, info.OriginOwnerPoolName)
		return false
	}

	if poolResource.isolatedResources+poolResource.podResources[info.PodUID] > poolResource.maxResources {
		general.Warningf("pod %v container %v can't be isolated: exceeds resource-ratio, current %v, max %v",
			info.PodName, info.ContainerName, poolResource.isolatedResources, poolResource.maxResources)
		return false
	}
	if poolResource.isolatedPods.Len()+1 > poolResource.maxPods {
		general.Warningf("pod %v container %v can't be isolated: exceeds pod-ratio, current %v, max %v",
			info.PodName, info.ContainerName, poolResource.isolatedPods.Len(), poolResource.maxPods)
		return false
	}

	return true
}

// initIsolationStates init poolIsolationStates for each pool
func (l *LoadIsolator) initIsolationStates() map[string]*poolIsolationStates {
	// sum up the total resources and construct isolation resources for each pool
	isolationResources := make(map[string]*poolIsolationStates)
	l.metaReader.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		if !checkTargetContainer(ci) {
			return true
		}

		isolationConfigKey := l.configTranslator.Translate(ci.OriginOwnerPoolName)
		// init for corresponding pool
		if _, ok := isolationResources[ci.OriginOwnerPoolName]; !ok {
			state := &poolIsolationStates{
				maxResourceRatio: l.conf.IsolatedMaxResourceRatio,
				maxPodRatio:      l.conf.IsolatedMaxPodRatio,
				podResources:     make(map[string]float64),
				isolatedPods:     sets.NewString(),
			}

			if ratio, ratioOK := l.conf.IsolatedMaxPoolResourceRatios[isolationConfigKey]; ratioOK {
				state.maxResourceRatio = ratio
			}
			if ratio, ratioOK := l.conf.IsolatedMaxPoolPodRatios[isolationConfigKey]; ratioOK {
				state.maxPodRatio = ratio
			}

			isolationResources[ci.OriginOwnerPoolName] = state
		}

		// init for corresponding pod
		if _, ok := isolationResources[ci.OriginOwnerPoolName].podResources[podUID]; !ok {
			isolationResources[ci.OriginOwnerPoolName].podResources[podUID] = 0.
		}

		r := getMaxContainerResource(ci)
		isolationResources[ci.OriginOwnerPoolName].totalResources += r
		isolationResources[ci.OriginOwnerPoolName].podResources[podUID] += r

		return true
	})

	for pool := range isolationResources {
		totalPods := len(isolationResources[pool].podResources)
		isolationResources[pool].maxPods = general.Min(
			totalPods-1,
			int(float32(totalPods)*isolationResources[pool].maxPodRatio),
		)
		isolationResources[pool].maxResources = isolationResources[pool].totalResources * float64(isolationResources[pool].maxResourceRatio)
	}
	return isolationResources
}

// getIsolationState gets isolation state info from local cache
func (l *LoadIsolator) getIsolationState(info *types.ContainerInfo) *containerIsolationState {
	meta := containerMeta(info)
	state, _ := l.states.LoadOrStore(meta, &containerIsolationState{})
	return state.(*containerIsolationState)
}

// getSortedContainerInfo sorts container info to make sure the results are stable
func (l *LoadIsolator) getSortedContainerInfo() (containerInfos []*types.ContainerInfo) {
	l.metaReader.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		containerInfos = append(containerInfos, ci)
		return true
	})

	sort.Slice(containerInfos, func(i, j int) bool {
		return containerMeta(containerInfos[i]) > containerMeta(containerInfos[j])
	})
	return
}

func containerMeta(info *types.ContainerInfo) string {
	return info.PodUID + "/" + info.ContainerName
}

// only shared pools are supported to be isolated
func checkTargetContainer(info *types.ContainerInfo) bool {
	return strings.HasPrefix(info.QoSLevel, consts.PodAnnotationQoSLevelSharedCores) && !info.RampUp
}

func getMaxContainerResource(ci *types.ContainerInfo) float64 {
	return general.MaxFloat64(ci.CPULimit, ci.CPURequest)
}
