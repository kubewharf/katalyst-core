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

type poolIsolationResources struct {
	// totalLimits records total cpu limits in this pool
	// podLimits records cpu limits for this pod-uid
	totalLimits float64
	podLimits   map[string]float64
	maxRatio    float64

	// isolatedPods records pod-uid that already isolated
	// isolatedLimits records total already-isolated cpu limits in this pool
	isolatedPods   sets.String
	isolatedLimits float64
}

// LoadIsolator decides isolation states based on cpu-load for containers
type LoadIsolator struct {
	conf *cpu.CPUIsolationConfiguration

	emitter    metrics.MetricEmitter
	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer

	// map from pod/container pair to containerIsolationState
	states sync.Map
}

func NewLoadIsolator(conf *config.Configuration, _ interface{}, emitter metrics.MetricEmitter,
	metaCache metacache.MetaReader, metaServer *metaserver.MetaServer) Isolator {
	return &LoadIsolator{
		conf: conf.CPUIsolationConfiguration,

		emitter:    emitter,
		metaReader: metaCache,
		metaServer: metaServer,
	}
}

func (l *LoadIsolator) GetIsolatedPods() []string {
	if l.conf.IsolationDisabled {
		return []string{}
	}

	isolationResources := l.initIsolationResources()
	for k, v := range isolationResources {
		general.Infof("initialized %v resource: %v", k, *v)
	}

	// walk through each container to judge whether it should be isolated
	uidSets := sets.NewString()
	existed := sets.NewString()
	for _, ci := range l.getSortedContainerInfo() {
		// if isolation is disabled from the certain pool, mark as none-isolated and trigger container clear
		if l.conf.IsolationDisabledPools.Has(ci.OriginOwnerPoolName) {
			continue
		}

		existed.Insert(containerMeta(ci))
		if l.checkContainerIsolated(ci, isolationResources) {
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
func (l *LoadIsolator) checkContainerIsolated(info *types.ContainerInfo, isolationResources map[string]*poolIsolationResources) (isolated bool) {
	defer func() {
		// record container limit
		if isolated && !isolationResources[info.OriginOwnerPoolName].isolatedPods.Has(info.PodUID) {
			isolationResources[info.OriginOwnerPoolName].isolatedPods.Insert(info.PodUID)
			isolationResources[info.OriginOwnerPoolName].isolatedLimits += isolationResources[info.OriginOwnerPoolName].podLimits[info.PodUID]
		}
	}()

	if !checkTargetContainer(info) {
		return false
	} else if !l.checkIsolationPoolLimit(info, isolationResources) {
		general.Warningf("pod %v container %v can't be isolated cause it will exceeds limit ratio", info.PodName, info.ContainerName)
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

	loadBeyondTarget := m.Value > info.CPULimit*float64(l.conf.IsolationCPURatio) || m.Value > info.CPULimit+float64(l.conf.IsolationCPUSize)
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

// checkIsolationPoolLimit returns true if this container can be isolated
// aspect of the limitation of total isolated containers in this pool
func (l *LoadIsolator) checkIsolationPoolLimit(info *types.ContainerInfo, isolationResources map[string]*poolIsolationResources) bool {
	poolResource := isolationResources[info.OriginOwnerPoolName]
	if poolResource.isolatedLimits+poolResource.podLimits[info.PodUID] > poolResource.totalLimits*poolResource.maxRatio {
		return false
	}
	return true
}

// initIsolationResources init poolIsolationResources for each pool
func (l *LoadIsolator) initIsolationResources() map[string]*poolIsolationResources {
	// sum up the total limits and construct isolation resources for each pool
	isolationResources := make(map[string]*poolIsolationResources)
	l.metaReader.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		if checkTargetContainer(ci) {
			// init for corresponding pool
			if _, ok := isolationResources[ci.OriginOwnerPoolName]; !ok {
				maxRatio := l.conf.IsolatedMaxRatios
				if ratio, ratioOK := l.conf.IsolatedMaxPoolRatios[ci.OriginOwnerPoolName]; ratioOK {
					maxRatio = ratio
				}

				isolationResources[ci.OriginOwnerPoolName] = &poolIsolationResources{
					maxRatio:     float64(maxRatio),
					podLimits:    make(map[string]float64),
					isolatedPods: sets.NewString(),
				}
			}

			// init for corresponding pod
			if _, ok := isolationResources[ci.OriginOwnerPoolName].podLimits[podUID]; !ok {
				isolationResources[ci.OriginOwnerPoolName].podLimits[podUID] = 0.
			}

			isolationResources[ci.OriginOwnerPoolName].totalLimits += ci.CPULimit
			isolationResources[ci.OriginOwnerPoolName].podLimits[podUID] += ci.CPULimit
		}
		return true
	})
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
