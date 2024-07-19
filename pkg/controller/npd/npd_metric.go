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

package npd

import (
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	metricsNameSyncNPDStatus = "sync_npd_status"
)

func (nc *NPDController) npdWorker() {
	for nc.processNextNPDItem() {
	}
}

func (nc *NPDController) processNextNPDItem() bool {
	key, quit := nc.metricsManager.GetNodeProfileStatusQueue().Get()
	if quit {
		klog.Warningf("[npd] NodeProfileStatusQueue quit")
		return false
	}
	defer nc.metricsManager.GetNodeProfileStatusQueue().Done(key)

	nodeName, ok := key.(string)
	if !ok {
		klog.Errorf("[spd] unknown data from NodeProfileStatusQueue: %v", key)
		nc.metricsManager.GetNodeProfileStatusQueue().Forget(key)
		return true
	}

	err := nc.syncStatus(nodeName)
	if err == nil {
		nc.metricsManager.GetNodeProfileStatusQueue().Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %v fail with %v", key, err))
	nc.metricsManager.GetNodeProfileStatusQueue().AddRateLimited(key)
	return true
}

func (nc *NPDController) syncStatus(nodeName string) error {
	klog.V(6).Infof("[npd] sync node %v npd status", nodeName)

	status := nc.metricsManager.GetNodeProfileStatus(nodeName)
	if status == nil {
		klog.Warningf("[npd] get node %v npd status nil", nodeName)
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		npd, err := nc.npdLister.Get(nodeName)
		if err != nil {
			klog.Errorf("[npd] failed to get npd %v: %v", nodeName, err)
			return err
		}

		npdCopy := npd.DeepCopy()
		nc.mergeMetricsStatus(npdCopy, *status)
		if apiequality.Semantic.DeepEqual(npd.Status, npdCopy.Status) {
			return nil
		}

		_, err = nc.npdControl.UpdateNPDStatus(nc.ctx, npdCopy, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("[npd] failed to update npd status for node %v: %v", nodeName, err)
			return err
		}

		klog.V(10).Infof("[npd] node %v npd status update to %+v", nodeName, npdCopy.Status)
		_ = nc.metricsEmitter.StoreInt64(metricsNameSyncNPDStatus, 1, metrics.MetricTypeNameCount, metrics.MetricTag{
			Key: "status", Val: "success",
		})

		return nil
	})
	if err != nil {
		klog.Errorf("[npd] faild to update npd status for node %v: %v", nodeName, err)
		_ = nc.metricsEmitter.StoreInt64(metricsNameSyncNPDStatus, 1, metrics.MetricTypeNameCount, metrics.MetricTag{
			Key: "status", Val: "failed",
		})
		return err
	}
	return nil
}

func (nc *NPDController) mergeMetricsStatus(npd *v1alpha1.NodeProfileDescriptor, expected v1alpha1.NodeProfileDescriptorStatus) {
	for _, nodeMetric := range expected.NodeMetrics {
		util.InsertNPDScopedNodeMetrics(&npd.Status, &nodeMetric)
	}

	for _, podMetric := range expected.PodMetrics {
		util.InsertNPDScopedPodMetrics(&npd.Status, &podMetric)
	}

	for i := 0; i < len(npd.Status.NodeMetrics); i++ {
		if _, ok := nc.supportedNodeScopes[npd.Status.NodeMetrics[i].Scope]; !ok {
			klog.Infof("skip npd %v node metric with unsupported scope %v", npd.Name, npd.Status.NodeMetrics[i].Scope)
			npd.Status.NodeMetrics = append(npd.Status.NodeMetrics[:i], npd.Status.NodeMetrics[i+1:]...)
		}
	}

	for i := 0; i < len(npd.Status.PodMetrics); i++ {
		if _, ok := nc.supportedPodScopes[npd.Status.PodMetrics[i].Scope]; !ok {
			klog.Infof("skip npd %v pod metric with unsupported scope %v", npd.Name, npd.Status.PodMetrics[i].Scope)
			npd.Status.PodMetrics = append(npd.Status.PodMetrics[:i], npd.Status.PodMetrics[i+1:]...)
		}
	}
}
