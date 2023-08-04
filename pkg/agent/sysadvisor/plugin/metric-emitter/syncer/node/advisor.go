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

package node

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"

	apimetricnode "github.com/kubewharf/katalyst-api/pkg/metric/node"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func (n *MetricSyncerNode) advisorMetric(ctx context.Context) {
	tags := n.generateMetricTag(ctx)
	general.InfofV(4, "get metric advisor metric for node")

	now := time.Now().UnixMilli()
	n.metaReader.RangeRegionInfo(func(regionName string, regionInfo *types.RegionInfo) bool {
		if regionInfo == nil {
			general.Errorf("nil regionInfo %v", regionName)
			return true
		}

		// put indicator-overshot infos and region-bound infos in metric tags
		regionTag := append(tags,
			[]metrics.MetricTag{
				{
					Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
					Val: fmt.Sprintf("%v", now),
				},
				{
					Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "region"),
					Val: fmt.Sprintf("%v", regionName),
				},
				{
					Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "bound"),
					Val: fmt.Sprintf("%v", regionInfo.RegionStatus.BoundType),
				},
			}...)
		for indicator, overshot := range regionInfo.RegionStatus.OvershootStatus {
			regionTag = append(tags, metrics.MetricTag{
				Key: fmt.Sprintf("%s%s%s", data.CustomMetricLabelSelectorPrefixKey, "indicator_", indicator),
				Val: fmt.Sprintf("%v", overshot),
			})
		}

		_ = n.dataEmitter.StoreFloat64(apimetricnode.CustomMetricNodeAdvisorKnobStatus, 1, metrics.MetricTypeNameRaw, regionTag...)
		return true
	})

	pod2Pools := make(map[string]string)
	n.metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		pod2Pools[podUID] = containerInfo.OwnerPoolName
		return true
	})

	pool2Pods := make(map[string][]*v1.Pod)
	_, err := n.metaServer.GetPodList(ctx, func(pod *v1.Pod) bool {
		if pool, ok := pod2Pools[string(pod.UID)]; ok {
			pool2Pods[pool] = append(pool2Pods[pool], pod)
		}
		return false
	})
	if err != nil {
		general.Errorf("get podList from metaServer failed: %v", err)
		return
	}

	for pool, pods := range pool2Pods {
		v := n.metaServer.AggregatePodMetric(pods, pkgconsts.MetricLoad1MinContainer, metric.AggregatorSum, metric.DefaultContainerMetricFilter)
		_ = n.dataEmitter.StoreFloat64(apimetricnode.CustomMetricNodeAdvisorPoolLoad1Min, v.Value, metrics.MetricTypeNameRaw, append(tags,
			[]metrics.MetricTag{
				{
					Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
					Val: fmt.Sprintf("%v", v.Time.UnixMilli()),
				},
				{
					Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "pool"),
					Val: fmt.Sprintf("%v", pool),
				},
			}...)...)
	}

}
