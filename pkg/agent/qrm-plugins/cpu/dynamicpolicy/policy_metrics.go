/*
Copyright 2026 The Katalyst Authors.

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
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type podPoolMetricIdentity struct {
	namespace string
	name      string
	numaID    int
}

func (p *DynamicPolicy) emitFinalPoolSizeMetrics(entries state.PodEntries) {
	isolationOwners := isolationPoolOwners(entries)
	for poolName, containerEntries := range entries {
		if !containerEntries.IsPoolEntry() {
			continue
		}

		allocationInfo := containerEntries[commonstate.FakedContainerName]
		if allocationInfo == nil {
			general.Warningf("skip emitting pool size for malformed pool %s", poolName)
			continue
		}

		owner := allocationInfo
		if commonstate.GetPoolType(commonstate.OwnerPoolNameTranslator.Translate(poolName)) ==
			commonstate.PoolNamePrefixIsolation {
			owner = isolationOwners[poolName]
			if owner == nil {
				general.Warningf("skip emitting pool size for isolation pool %s without pod owner", poolName)
				continue
			}
		}
		for numaID, cpus := range allocationInfo.TopologyAwareAssignments {
			p.emitPoolSizeMetric(poolName, poolSizeMetricPoolName(poolName, owner), numaID, cpus.Size())
		}
	}

	// dedicated allocations are stored as pod-uid keyed container entries rather
	// than pool entries, so they are skipped by the loop above. Union all main
	// container assignments by pod and NUMA before emitting one pod-scoped series.
	dedicatedByIdentity := make(map[podPoolMetricIdentity]machine.CPUSet)
	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil || !allocationInfo.CheckDedicated() || !allocationInfo.CheckMainContainer() {
				continue
			}
			for numaID, cpus := range allocationInfo.TopologyAwareAssignments {
				identity := podPoolMetricIdentity{
					namespace: allocationInfo.PodNamespace,
					name:      allocationInfo.PodName,
					numaID:    numaID,
				}
				dedicatedByIdentity[identity] = dedicatedByIdentity[identity].Union(cpus)
			}
		}
	}
	for identity, cpus := range dedicatedByIdentity {
		p.emitPoolSizeMetric(commonstate.PoolNameDedicated,
			podScopedPoolMetricName(commonstate.PoolNameDedicated, identity.namespace, identity.name),
			identity.numaID, cpus.Size())
	}
}

func (p *DynamicPolicy) emitPoolSizeMetric(poolName, formattedPoolName string, numaID, size int) {
	_ = p.emitter.emitter.StoreInt64(util.MetricNamePoolSize, int64(size),
		metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "poolName", Val: poolName},
		metrics.MetricTag{Key: "pool_type", Val: commonstate.GetPoolType(poolName)},
		metrics.MetricTag{Key: "pool_name", Val: formattedPoolName},
		metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)})
}

func poolSizeMetricPoolName(poolName string, allocationInfo *state.AllocationInfo) string {
	poolType := commonstate.GetPoolType(commonstate.OwnerPoolNameTranslator.Translate(poolName))
	if (poolType == commonstate.PoolNameDedicated || poolType == commonstate.PoolNamePrefixIsolation) && allocationInfo != nil {
		return podScopedPoolMetricName(poolType, allocationInfo.PodNamespace, allocationInfo.PodName)
	}
	return metricutil.MetricTagValueFormat(poolName)
}

func isolationPoolOwners(entries state.PodEntries) map[string]*state.AllocationInfo {
	owners := make(map[string]*state.AllocationInfo)
	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}
			poolName := allocationInfo.OwnerPoolName
			if commonstate.GetPoolType(commonstate.OwnerPoolNameTranslator.Translate(poolName)) !=
				commonstate.PoolNamePrefixIsolation {
				continue
			}
			owners[poolName] = allocationInfo
		}
	}
	return owners
}

func podScopedPoolMetricName(poolType, namespace, podName string) string {
	raw := strings.ReplaceAll(poolType+"-"+namespace+"/"+podName, " ", "_")
	if len([]rune(raw)) <= metricutil.MaxTagLength {
		return raw
	}

	sum := sha256.Sum256([]byte(raw))
	suffix := "-" + hex.EncodeToString(sum[:6])
	runes := []rune(raw)
	return string(runes[:metricutil.MaxTagLength-len(suffix)]) + suffix
}
