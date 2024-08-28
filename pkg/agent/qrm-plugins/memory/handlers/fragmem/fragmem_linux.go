//go:build linux
// +build linux

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

package fragmem

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/errors"

	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

var (
	delayTimes int = 1
	mu         sync.RWMutex
)

// SetDelayValue sets the value of the global delayTimes
func SetDelayTimes(value int) {
	mu.Lock()
	defer mu.Unlock()
	delayTimes = value
}

// GetDelayValue returns the value of the global delayTimes
func GetDelayTimes() int {
	mu.RLock()
	defer mu.RUnlock()
	return delayTimes
}

func isHighSystemLoad(metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) bool {
	load, err := helper.GetNodeMetricWithTime(metaServer.MetricsFetcher, emitter, consts.MetricLoad5MinSystem)
	if err != nil {
		general.Errorf("Failed to get load: %v", err)
		return false
	}

	numCPU := metaServer.CPUTopology.NumCPUs
	loadPerCPU := int(load.Value) * 100 / numCPU
	general.Infof("Host load info: load: %v, numCPU: %v, loadPerCPU: %v", load.Value, numCPU, loadPerCPU)

	return loadPerCPU > minHostLoad
}

func memCompacWithBestEffort(conf *coreconfig.Configuration, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) {
	fragScoreWatermark := uint64(general.Clamp(float64(conf.SetMemFragScoreAsync), fragScoreMin, fragScoreMax))
	for _, numaID := range metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		// Step 1.0, get fragScore from Malachite
		score, err := helper.GetNumaMetricWithTime(metaServer.MetricsFetcher, emitter, consts.MetricMemFragScoreNuma, numaID)
		if err != nil {
			general.Errorf("Failed to get frag score:%v", err)
			continue
		}
		fragScore := int(score.Value)
		general.Infof("NUMA fragScore info: node:%d, fragScore:%d, fragScoreGate:%d", numaID, fragScore, fragScoreWatermark)
		// Step 1.1, if the fragScore exceeds the target, we will initiate memory compaction
		if fragScore < int(fragScoreWatermark) {
			continue
		}

		// Step 2, check if kcompactd is in D state
		if process.IsCommandInDState(commandKcompactd) {
			general.Infof("kcompactd is in D state")
			return
		}

		// Step 3, do memory compaction in node level
		_ = emitter.StoreInt64(metricNameMemoryCompaction, 1, metrics.MetricTypeNameRaw)
		setHostMemCompact(numaID)
		time.Sleep(sleepCompactTime * time.Second)

		// Step 4, if memory compaction is not effective, extend the check interval
		/*
		 * If mem fragScore shows no significant improvement after compaction
		 * (frag_score < (fragScoreWatermark-minFragScoreGap)),
		 * it indicates that the system has minimal physical memory fragmentation
		 */
		newScore, err := helper.GetNumaMetricWithTime(metaServer.MetricsFetcher, emitter, consts.MetricMemFragScoreNuma, numaID)
		if err != nil {
			continue
		}
		newFragScore := int(newScore.Value)
		general.Infof("Node fragScore new info: node:%d, fragScore:%d", numaID, newFragScore)
		// compare the new and old fragScore to avoid ineffective compaction.
		if newFragScore >= int(fragScoreWatermark-minFragScoreGap) {
			general.Infof("Not so much memory fragmentation in this node, increase the scanning cycle")
			SetDelayTimes(delayCompactTimes)
		}
	}
}

/* SetMemCompact is the unified solution for memory compaction.
* it includes 3 parts:
* 1, set the threshold of fragmentation score that triggers synchronous memory compaction in the memory slow path.
* 2, if has proactive compaction feature, then set the threshold of fragmentation score for asynchronous memory compaction through compaction_proactiveness.
* 3, if no proactive compaction feature, then use the threshold of fragmentation score to trigger manually memory compaction.
 */
func SetMemCompact(conf *coreconfig.Configuration,
	_ interface{}, _ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
) {
	general.Infof("called")

	var errList []error
	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.SetMemCompact, errors.NewAggregate(errList))
	}()

	if conf == nil || emitter == nil || metaServer == nil {
		general.Errorf("nil input, conf:%v, emitter:%v, metaServer:%v", conf, emitter, metaServer)
		return
	}

	if delay := GetDelayTimes(); delay > 0 {
		general.Infof("No memory fragmentation in this node, skip this scanning cycle, delay=%d", delay)
		SetDelayTimes(delay - 1)
		return
	}

	// EnableSettingMemCompact featuregate.
	if !conf.EnableSettingFragMem {
		general.Infof("EnableSettingFragMem disabled")
		return
	}

	// Step1, check proactive compaction.
	// if proactive compaction feature enabled, then return.
	if !checkCompactionProactivenessDisabled(hostCompactProactivenessFile) {
		general.Infof("proactive compaction enabled, then return")
		return
	}

	// Step2, avoid too much host pressure.
	if isHighSystemLoad(metaServer, emitter) {
		return
	}

	// Step3, user space memory compaction will be trigger while exceeding fragScoreWatermark(default:80).
	memCompacWithBestEffort(conf, metaServer, emitter)
}
