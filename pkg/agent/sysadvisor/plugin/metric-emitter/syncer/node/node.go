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
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	apimetricnode "github.com/kubewharf/katalyst-api/pkg/metric/node"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/syncer"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/types"
	sysadvisortypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// nodeRawMetricNameMapping maps the raw metricName (collected from agent.MetricsFetcher)
// to the standard metricName (used by custom-metric-api-server)
var nodeRawMetricNameMapping = map[string]string{
	consts.MetricCPUTotalSystem:      apimetricnode.CustomMetricNodeCPUTotal,
	consts.MetricCPUUsageSystem:      apimetricnode.CustomMetricNodeCPUUsage,
	consts.MetricCPUUsageRatioSystem: apimetricnode.CustomMetricNodeCPUUsageRatio,
	consts.MetricLoad1MinSystem:      apimetricnode.CustomMetricNodeCPULoad1Min,

	consts.MetricMemFreeSystem:      apimetricnode.CustomMetricNodeMemoryFree,
	consts.MetricMemAvailableSystem: apimetricnode.CustomMetricNodeMemoryAvailable,
}

// nodeNUMARawMetricNameMapping maps the raw metricName (collected from agent.MetricsFetcher)
// to the standard metricName (used by custom-metric-api-server)
var nodeNUMARawMetricNameMapping = map[string]string{
	consts.MetricTotalPsMemBandwidthNuma: apimetricnode.CustomMetricNUMAMemoryBandwidthTotal,
}

// nodeCachedMetricNameMapping maps the cached metricName (processed by plugin.SysAdvisorPlugin)
// to the standard metricName (used by custom-metric-api-server)

type MetricSyncerNode struct {
	metricMapping     map[string]string
	numaMetricMapping map[string]string

	conf *metricemitter.MetricEmitterPluginConfiguration
	node *v1.Node

	metricEmitter metrics.MetricEmitter
	dataEmitter   metrics.MetricEmitter

	metaServer *metaserver.MetaServer
	metaReader metacache.MetaReader
}

func NewMetricSyncerNode(conf *config.Configuration, _ interface{},
	metricEmitter metrics.MetricEmitter, emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer, metaReader metacache.MetaReader,
) (syncer.CustomMetricSyncer, error) {
	dataEmitter, err := emitterPool.GetMetricsEmitter(metricspool.PrometheusMetricOptions{
		Path: metrics.PrometheusMetricPathNameCustomMetric,
	})
	if err != nil {
		klog.Errorf("[cus-metric-emitter] failed to init metric emitter: %v", err)
		return nil, err
	}

	metricMapping := general.MergeMap(nodeRawMetricNameMapping, conf.MetricEmitterNodeConfiguration.MetricMapping)
	numaMetricMapping := general.MergeMap(nodeNUMARawMetricNameMapping, conf.MetricEmitterNodeConfiguration.NUMAMetricMapping)

	return &MetricSyncerNode{
		metricMapping:     metricMapping,
		numaMetricMapping: numaMetricMapping,

		conf: conf.AgentConfiguration.MetricEmitterPluginConfiguration,

		metricEmitter: metricEmitter,
		dataEmitter:   dataEmitter,
		metaServer:    metaServer,
		metaReader:    metaReader,
	}, nil
}

func (n *MetricSyncerNode) Name() string {
	return types.MetricSyncerNameNode
}

func (n *MetricSyncerNode) Run(ctx context.Context) {
	rChan := make(chan metrictypes.NotifiedResponse, 20)
	rNUMAChan := make(chan metrictypes.NotifiedResponse, 20)
	go n.receiveRawNode(ctx, rChan)
	go n.receiveRawNUMA(ctx, rNUMAChan)
	go wait.Until(func() { n.advisorMetric(ctx) }, time.Second*3, ctx.Done())

	// there is no need to deRegister for node-related metric
	for rawMetricName := range n.metricMapping {
		klog.Infof("register raw node metric: %v", rawMetricName)
		n.metaServer.MetricsFetcher.RegisterNotifier(metrictypes.MetricsScopeNode, metrictypes.NotifiedRequest{
			MetricName: rawMetricName,
		}, rChan)
	}

	// there is no need to deRegister for numa-related metric
	for rawNUMAMetricName := range n.numaMetricMapping {
		for _, numaID := range n.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
			klog.Infof("register raw node numa metric: %v, numa: %d", rawNUMAMetricName, numaID)
			n.metaServer.MetricsFetcher.RegisterNotifier(metrictypes.MetricsScopeNuma, metrictypes.NotifiedRequest{
				MetricName: rawNUMAMetricName,
				NumaID:     numaID,
			}, rNUMAChan)
		}
	}
}

// receiveRawNode receives notified response from raw data source
func (n *MetricSyncerNode) receiveRawNode(ctx context.Context, rChan chan metrictypes.NotifiedResponse) {
	for {
		select {
		case response := <-rChan:
			if response.Req.MetricName == "" {
				continue
			} else if response.Time == nil {
				continue
			}

			targetMetricName, ok := n.metricMapping[response.Req.MetricName]
			if !ok {
				klog.Warningf("invalid node raw metric name: %v", response.Req.MetricName)
				continue
			}

			klog.V(4).Infof("get metric %v for node", response.Req.MetricName)
			if tags := n.generateMetricTagNode(ctx); len(tags) > 0 {
				_ = n.dataEmitter.StoreFloat64(targetMetricName, response.Value, metrics.MetricTypeNameRaw, append(tags,
					metrics.MetricTag{
						Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
						Val: fmt.Sprintf("%v", response.Time.UnixMilli()),
					})...)
			}
		case <-ctx.Done():
			klog.Infof("metric emitter for node has been stopped")
			return
		}
	}
}

// receiveRawNUMA receives numa notified response from raw data source
func (n *MetricSyncerNode) receiveRawNUMA(ctx context.Context, rChan chan metrictypes.NotifiedResponse) {
	for {
		select {
		case response := <-rChan:
			if response.Req.MetricName == "" {
				continue
			} else if response.Time == nil {
				continue
			}

			targetMetricName, ok := n.numaMetricMapping[response.Req.MetricName]
			if !ok {
				klog.Warningf("invalid node numa raw metric name: %v", response.Req.MetricName)
				continue
			}
			klog.V(4).Infof("get metric %v for node %s, numa id %d, value %f", response.Req.MetricName, response.Req.NumaNode, response.Req.NumaID, response.Value)
			if tags := n.generateMetricTagNuma(ctx, response.Req.NumaID); len(tags) > 0 {
				_ = n.dataEmitter.StoreFloat64(targetMetricName, response.Value, metrics.MetricTypeNameRaw, append(tags,
					metrics.MetricTag{
						Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
						Val: fmt.Sprintf("%v", response.Time.UnixMilli()),
					},
					metrics.MetricTag{
						Key: fmt.Sprintf("%snuma", data.CustomMetricLabelSelectorPrefixKey),
						Val: strconv.Itoa(response.Req.NumaID),
					})...)
			}
		case <-ctx.Done():
			klog.Infof("metric emitter for node numa has been stopped")
			return

		}
	}
}

// generateMetricTag generates common tags that are bounded to current Node object
func (n *MetricSyncerNode) generateMetricTag(ctx context.Context) (tags []metrics.MetricTag) {
	if n.node == nil && n.metaServer != nil && n.metaServer.NodeFetcher != nil {
		node, err := n.metaServer.GetNode(ctx)
		if err != nil {
			klog.Warningf("get current node failed: %v", err)
			return
		}
		n.node = node
	}

	if n.node != nil {
		tags = []metrics.MetricTag{
			{
				Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyObject),
				Val: "nodes",
			},
			{
				Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyObjectName),
				Val: n.node.Name,
			},
		}
		for key, value := range n.node.Labels {
			if n.conf.NodeMetricLabel.Has(key) {
				tags = append(tags, metrics.MetricTag{
					Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, key),
					Val: value,
				})
			}
		}
	}

	// append cpu codename info
	tags = append(tags, metrics.MetricTag{
		Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "cpu_codename"),
		Val: helper.GetCpuCodeName(n.metaServer.MetricsFetcher),
	})

	// append vendor info
	_, isVmStr := helper.GetIsVM(n.metaServer.MetricsFetcher)

	tags = append(tags, metrics.MetricTag{
		Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "is_vm"),
		Val: isVmStr,
	})
	return tags
}

// generateMetricTag generates tags that are bounded to current Node object
func (n *MetricSyncerNode) generateMetricTagNode(ctx context.Context) (tags []metrics.MetricTag) {
	tags = n.generateMetricTag(ctx)
	// append node numa bit mask
	numas := n.metaServer.KatalystMachineInfo.CPUDetails.NUMANodes()
	numaBitMask := sysadvisortypes.NumaIDBitMask(numas.ToSliceInt())
	tags = append(tags, metrics.MetricTag{
		Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "numa_bit_mask"),
		Val: fmt.Sprintf("%d", numaBitMask),
	})
	return tags
}

// generateMetricTag generates tags that are bounded to current numa node object
func (n *MetricSyncerNode) generateMetricTagNuma(ctx context.Context, numaID int) (tags []metrics.MetricTag) {
	tags = n.generateMetricTag(ctx)
	numaBitMask := sysadvisortypes.NumaIDBitMask([]int{numaID})
	tags = append(tags, metrics.MetricTag{
		Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "numa_bit_mask"),
		Val: fmt.Sprintf("%d", numaBitMask),
	})
	return tags
}
