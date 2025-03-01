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

package kubelet

import (
	"context"
	"sync"

	"k8s.io/klog/v2"
	statsapi "k8s.io/kubelet/pkg/apis/stats/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/kubelet/client"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	metricsNamKubeletSummaryUnHealthy = "kubelet_summary_unhealthy"
)

func NewKubeletSummaryProvisioner(baseConf *global.BaseConfiguration, _ *metaserver.MetricConfiguration,
	emitter metrics.MetricEmitter, _ pod.PodFetcher, metricStore *utilmetric.MetricStore, _ *machine.KatalystMachineInfo,
) types.MetricsProvisioner {
	return &KubeletSummaryProvisioner{
		metricStore: metricStore,
		emitter:     emitter,
		client:      client.NewKubeletSummaryClient(baseConf),
	}
}

type KubeletSummaryProvisioner struct {
	metricStore *utilmetric.MetricStore
	emitter     metrics.MetricEmitter
	client      *client.KubeletSummaryClient

	startOnce sync.Once
	hasSynced bool
}

func (p *KubeletSummaryProvisioner) Run(ctx context.Context) {
	p.sample(ctx)
}

func (p *KubeletSummaryProvisioner) sample(ctx context.Context) {
	summary, err := p.client.Summary(ctx)
	if err != nil {
		klog.Errorf("failed to update stats/summary from kubelet: %q", err)
		_ = p.emitter.StoreInt64(metricsNamKubeletSummaryUnHealthy, 1, metrics.MetricTypeNameRaw)
		return
	}

	p.processNodeFsStats(summary.Node.Fs)
	if summary.Node.Runtime != nil {
		p.processImageFsStats(summary.Node.Runtime.ImageFs)
	}

	for _, podStats := range summary.Pods {
		for _, volumeStats := range podStats.VolumeStats {
			p.processVolumeStats(podStats.PodRef.UID, &volumeStats)
		}

		for _, containerStats := range podStats.Containers {
			p.processContainerRootfsStats(podStats.PodRef.UID, &containerStats)
			p.processContainerLogsStats(podStats.PodRef.UID, &containerStats)
		}
	}
}

func (p *KubeletSummaryProvisioner) processNodeFsStats(nodeFsStats *statsapi.FsStats) {
	if nodeFsStats == nil {
		return
	}
	updateTime := nodeFsStats.Time.Time
	if nodeFsStats.AvailableBytes != nil {
		p.metricStore.SetNodeMetric(consts.MetricsNodeFsAvailable, utilmetric.MetricData{Value: float64(*nodeFsStats.AvailableBytes), Time: &updateTime})
	}
	if nodeFsStats.CapacityBytes != nil {
		p.metricStore.SetNodeMetric(consts.MetricsNodeFsCapacity, utilmetric.MetricData{Value: float64(*nodeFsStats.CapacityBytes), Time: &updateTime})
	}
	if nodeFsStats.UsedBytes != nil {
		p.metricStore.SetNodeMetric(consts.MetricsNodeFsUsed, utilmetric.MetricData{Value: float64(*nodeFsStats.UsedBytes), Time: &updateTime})
	}
	if nodeFsStats.InodesFree != nil {
		p.metricStore.SetNodeMetric(consts.MetricsNodeFsInodesFree, utilmetric.MetricData{Value: float64(*nodeFsStats.InodesFree), Time: &updateTime})
	}
	if nodeFsStats.InodesUsed != nil {
		p.metricStore.SetNodeMetric(consts.MetricsNodeFsInodesUsed, utilmetric.MetricData{Value: float64(*nodeFsStats.InodesUsed), Time: &updateTime})
	}
	if nodeFsStats.Inodes != nil {
		p.metricStore.SetNodeMetric(consts.MetricsNodeFsInodes, utilmetric.MetricData{Value: float64(*nodeFsStats.Inodes), Time: &updateTime})
	}
}

func (p *KubeletSummaryProvisioner) processImageFsStats(imageFsStats *statsapi.FsStats) {
	if imageFsStats == nil {
		return
	}
	updateTime := imageFsStats.Time.Time
	if imageFsStats.AvailableBytes != nil {
		p.metricStore.SetNodeMetric(consts.MetricsImageFsAvailable, utilmetric.MetricData{Value: float64(*imageFsStats.AvailableBytes), Time: &updateTime})
	}
	if imageFsStats.CapacityBytes != nil {
		p.metricStore.SetNodeMetric(consts.MetricsImageFsCapacity, utilmetric.MetricData{Value: float64(*imageFsStats.CapacityBytes), Time: &updateTime})
	}
	if imageFsStats.UsedBytes != nil {
		p.metricStore.SetNodeMetric(consts.MetricsImageFsUsed, utilmetric.MetricData{Value: float64(*imageFsStats.UsedBytes), Time: &updateTime})
	}
	if imageFsStats.InodesFree != nil {
		p.metricStore.SetNodeMetric(consts.MetricsImageFsInodesFree, utilmetric.MetricData{Value: float64(*imageFsStats.InodesFree), Time: &updateTime})
	}
	if imageFsStats.InodesUsed != nil {
		p.metricStore.SetNodeMetric(consts.MetricsImageFsInodesUsed, utilmetric.MetricData{Value: float64(*imageFsStats.InodesUsed), Time: &updateTime})
	}
	if imageFsStats.Inodes != nil {
		p.metricStore.SetNodeMetric(consts.MetricsImageFsInodes, utilmetric.MetricData{Value: float64(*imageFsStats.Inodes), Time: &updateTime})
	}
}

func (p *KubeletSummaryProvisioner) processVolumeStats(podUID string, volumeStats *statsapi.VolumeStats) {
	updateTime := volumeStats.Time.Time
	if volumeStats.AvailableBytes != nil {
		p.metricStore.SetPodVolumeMetric(podUID, volumeStats.Name, consts.MetricsPodVolumeAvailable, utilmetric.MetricData{Value: float64(*volumeStats.AvailableBytes), Time: &updateTime})
	}
	if volumeStats.CapacityBytes != nil {
		p.metricStore.SetPodVolumeMetric(podUID, volumeStats.Name, consts.MetricsPodVolumeCapacity, utilmetric.MetricData{Value: float64(*volumeStats.CapacityBytes), Time: &updateTime})
	}
	if volumeStats.UsedBytes != nil {
		p.metricStore.SetPodVolumeMetric(podUID, volumeStats.Name, consts.MetricsPodVolumeUsed, utilmetric.MetricData{Value: float64(*volumeStats.UsedBytes), Time: &updateTime})
	}
	if volumeStats.Inodes != nil {
		p.metricStore.SetPodVolumeMetric(podUID, volumeStats.Name, consts.MetricsPodVolumeInodes, utilmetric.MetricData{Value: float64(*volumeStats.Inodes), Time: &updateTime})
	}
	if volumeStats.InodesFree != nil {
		p.metricStore.SetPodVolumeMetric(podUID, volumeStats.Name, consts.MetricsPodVolumeInodesFree, utilmetric.MetricData{Value: float64(*volumeStats.InodesFree), Time: &updateTime})
	}
	if volumeStats.InodesUsed != nil {
		p.metricStore.SetPodVolumeMetric(podUID, volumeStats.Name, consts.MetricsPodVolumeInodesUsed, utilmetric.MetricData{Value: float64(*volumeStats.InodesUsed), Time: &updateTime})
	}
}

func (p *KubeletSummaryProvisioner) processContainerRootfsStats(podUID string, containerStats *statsapi.ContainerStats) {
	if containerStats.Rootfs == nil {
		return
	}
	updateTime := containerStats.Rootfs.Time.Time
	if containerStats.Rootfs.AvailableBytes != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsContainerRootfsAvailable, utilmetric.MetricData{Value: float64(*containerStats.Rootfs.AvailableBytes), Time: &updateTime})
	}
	if containerStats.Rootfs.CapacityBytes != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsContainerRootfsCapacity, utilmetric.MetricData{Value: float64(*containerStats.Rootfs.CapacityBytes), Time: &updateTime})
	}
	if containerStats.Rootfs.UsedBytes != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: float64(*containerStats.Rootfs.UsedBytes), Time: &updateTime})
	}
	if containerStats.Rootfs.Inodes != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsContainerRootfsInodes, utilmetric.MetricData{Value: float64(*containerStats.Rootfs.Inodes), Time: &updateTime})
	}
	if containerStats.Rootfs.InodesFree != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsContainerRootfsInodesFree, utilmetric.MetricData{Value: float64(*containerStats.Rootfs.InodesFree), Time: &updateTime})
	}
	if containerStats.Rootfs.InodesUsed != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsContainerRootfsInodesUsed, utilmetric.MetricData{Value: float64(*containerStats.Rootfs.InodesUsed), Time: &updateTime})
	}
}

func (p *KubeletSummaryProvisioner) processContainerLogsStats(podUID string, containerStats *statsapi.ContainerStats) {
	if containerStats.Logs == nil {
		return
	}
	updateTime := containerStats.Logs.Time.Time
	if containerStats.Logs.AvailableBytes != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsLogsAvailable, utilmetric.MetricData{Value: float64(*containerStats.Logs.AvailableBytes), Time: &updateTime})
	}
	if containerStats.Logs.CapacityBytes != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsLogsCapacity, utilmetric.MetricData{Value: float64(*containerStats.Logs.CapacityBytes), Time: &updateTime})
	}
	if containerStats.Logs.Inodes != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsLogsInodes, utilmetric.MetricData{Value: float64(*containerStats.Logs.Inodes), Time: &updateTime})
	}
	if containerStats.Logs.InodesFree != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsLogsInodesFree, utilmetric.MetricData{Value: float64(*containerStats.Logs.InodesFree), Time: &updateTime})
	}
	if containerStats.Logs.InodesUsed != nil {
		p.metricStore.SetContainerMetric(podUID, containerStats.Name, consts.MetricsLogsInodesUsed, utilmetric.MetricData{Value: float64(*containerStats.Logs.InodesUsed), Time: &updateTime})
	}
}
