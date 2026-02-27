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

package userwatermark

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	katalystcoreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// UserWatermarkReclaimManager is a memory reclamation manager based on the container's memory watermark at the user-space level.
type UserWatermarkReclaimManager struct {
	qosConfig   *generic.QoSConfiguration
	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
	dynamicConf *dynamicconfig.DynamicAgentConfiguration

	started             map[string]bool
	containerReclaimer  map[katalystcoreconsts.PodContainerName]Reclaimer
	cgroupPathReclaimer map[string]Reclaimer
}

// NewUserWatermarkReclaimManager creates a new instance of UserWatermarkReclaimManager.
func NewUserWatermarkReclaimManager(qosConfig *generic.QoSConfiguration, dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer) *UserWatermarkReclaimManager {
	return &UserWatermarkReclaimManager{
		emitter:     emitter,
		metaServer:  metaServer,
		qosConfig:   qosConfig,
		dynamicConf: dynamicConf,

		started:             make(map[string]bool),
		containerReclaimer:  make(map[katalystcoreconsts.PodContainerName]Reclaimer),
		cgroupPathReclaimer: make(map[string]Reclaimer),
	}
}

// Run starts the UserWatermarkReclaimManager periodically.
func (m *UserWatermarkReclaimManager) Run(stopCh chan struct{}) {
	wait.Until(m.reconcile, time.Duration(m.dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.ReconcileInterval)*time.Second, stopCh)
}

func (m *UserWatermarkReclaimManager) reconcile() {
	enabled := m.dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.EnableReclaimer
	if !enabled {
		general.Warningf("UserWatermarkReclaimManager is disabled")
		_ = m.emitter.StoreInt64(MetricNameUserWatermarkReclaimEnabled, 0, metrics.MetricTypeNameRaw)
		return
	}
	general.InfofV(5, "UserWatermarkReclaimManage start to reconcile")
	_ = m.emitter.StoreInt64(MetricNameUserWatermarkReclaimEnabled, 1, metrics.MetricTypeNameRaw)

	containerNamesMap := make(map[katalystcoreconsts.PodContainerName]bool)
	podList, err := m.metaServer.GetPodList(context.Background(), native.PodIsActive)
	if err != nil {
		general.Errorf("Failed to get pod list: %v", err)
		return
	}

	// iterate through all running containers to create a reclaimer instance for them
	for _, pod := range podList {
		if pod == nil {
			general.Errorf("Get nil pod from meta server.")
			continue
		}
		// calculate the pod qos level
		qos, err := m.qosConfig.GetQoSLevel(pod, map[string]string{})
		if err != nil {
			general.Errorf("Failed to get qos level for pod: %v, err: %v/%v", pod.Name, pod.Namespace, err)
			if helper.PodIsDaemonSet(pod) {
				qos = katalystapiconsts.PodAnnotationQoSLevelSystemCores
				general.Infof("DaemonSet pod %v is considered as system_cores qos level", pod.Name)
			}
		}
		// wrap container info
		for _, containerStatus := range pod.Status.ContainerStatuses {
			containerInfo := &types.ContainerInfo{
				PodUID:        string(pod.UID),
				PodName:       pod.Name,
				ContainerName: containerStatus.Name,
				Labels:        pod.Labels,
				Annotations:   pod.Annotations,
				QoSLevel:      qos,
			}
			containerID, err := m.metaServer.GetContainerID(containerInfo.PodUID, containerInfo.ContainerName)
			if err != nil || containerID == "" {
				general.Warningf("Failed to get container id for pod: %v, container name: %s, err: %v", pod.Name, containerInfo.ContainerName, err)
				continue
			}
			// get the container absolute cgroup path
			cgpath, err := GetContainerCgroupPath(containerInfo.PodName, containerID)
			if err != nil {
				general.Infof("Failed to get cgroup path for pod: %v, container name: %s, err: %v", pod.Name, containerInfo.ContainerName, err)
				continue
			}

			instanceInfo := ReclaimInstance{
				ContainerInfo: containerInfo,
				CgroupPath:    cgpath,
			}

			podContainerName := native.GeneratePodContainerName(containerInfo.PodName, containerInfo.ContainerName)
			containerNamesMap[podContainerName] = true
			_, exist := m.containerReclaimer[podContainerName]
			if !exist {
				m.containerReclaimer[podContainerName] = NewUserWatermarkReclaimer(instanceInfo, m.metaServer, m.emitter, m.dynamicConf)
				general.InfofV(5, "Create UserWatermarkReclaimer for pod: %v, container name: %s", pod.Name, containerInfo.ContainerName)
			}
		}
	}

	// update tmo config for specified cgroup paths
	for cgpath := range m.dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.CgroupConfig {
		general.Infof("Get Cgroup config for specific cgroup path %v", cgpath)
		if _, exist := m.cgroupPathReclaimer[cgpath]; !exist {
			instanceInfo := ReclaimInstance{
				ContainerInfo: &types.ContainerInfo{},
				CgroupPath:    cgpath,
			}
			m.cgroupPathReclaimer[cgpath] = NewUserWatermarkReclaimer(instanceInfo, m.metaServer, m.emitter, m.dynamicConf)
			general.InfofV(5, "Create UserWatermarkReclaimer for cgroup path: %v", cgpath)
		}
	}

	// delete user watermark reclaimer for not existed containers
	for podContainerName, reclaimer := range m.containerReclaimer {
		if _, exist := containerNamesMap[podContainerName]; !exist {
			reclaimer.Stop()
			delete(m.started, string(podContainerName))
			delete(m.containerReclaimer, podContainerName)
			general.Infof("Stop UserWatermarkReclaimer for pod container name: %v", podContainerName)
		}
	}

	// delete user watermark reclaimer for not existed cgroups
	for cgpath, reclaimer := range m.cgroupPathReclaimer {
		if _, exist := m.dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.CgroupConfig[cgpath]; !exist {
			reclaimer.Stop()
			delete(m.started, cgpath)
			delete(m.cgroupPathReclaimer, cgpath)
			general.Infof("Stop UserWatermarkReclaimer for cgroup path: %v", cgpath)
		}
	}

	// start reclaim reclaimer for each container
	for podContainerName, reclaimer := range m.containerReclaimer {
		if !m.started[string(podContainerName)] {
			go reclaimer.Run()
			m.started[string(podContainerName)] = true

			general.Infof("Start UserWatermarkReclaimer for pod container name: %v", podContainerName)
		}
		general.InfofV(5, "UserWatermarkReclaimer for pod container name: %v has started", podContainerName)
	}

	// start reclaim reclaimer for each cgroups
	for cgpath, reclaimer := range m.cgroupPathReclaimer {
		if !m.started[cgpath] {
			go reclaimer.Run()
			m.started[cgpath] = true

			general.Infof("Start UserWatermarkReclaimer for cgroup path: %v", cgpath)
		}
		general.InfofV(5, "UserWatermarkReclaimer for cgroup path: %v has started", cgpath)
	}
}
