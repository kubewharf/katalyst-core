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

package spd

import (
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	metricsNameSyncIndicatorSpec         = "sync_indicator_spec"
	metricsNameSyncIndicatorStatus       = "sync_indicator_status"
	metricsNameSyncIndicatorSpecCost     = "sync_indicator_spec_cost"
	metricsNameSyncIndicatorStatusCost   = "sync_indicator_status_cost"
	metricsNameIndicatorSpecChanLength   = "indicator_spec_chan_length"
	metricsNameIndicatorStatusChanLength = "indicator_status_chan_length"
)

func (sc *SPDController) syncIndicatorSpec() {
	c := sc.indicatorManager.GetIndicatorSpecChan()
	for {
		select {
		case nn, ok := <-c:
			if !ok {
				klog.Infof("[syncIndicatorSpec] indicator spec chan is closed")
				return
			}

			sc.syncSpec(nn)
			_ = sc.metricsEmitter.StoreInt64(metricsNameIndicatorSpecChanLength, int64(len(c)), metrics.MetricTypeNameRaw)
		case <-sc.ctx.Done():
			klog.Infoln("[syncIndicatorSpec] stop spd vpa queue worker.")
			return
		}
	}
}

func (sc *SPDController) syncSpec(nn types.NamespacedName) {
	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		klog.V(5).Infof("[spd] finished syncing indicator spec %q (%v)", nn, costs)
		_ = sc.metricsEmitter.StoreInt64(metricsNameSyncIndicatorSpecCost, costs.Microseconds(),
			metrics.MetricTypeNameRaw, metrics.MetricTag{Key: "name", Val: nn.String()})
	}()

	klog.V(5).Infof("[syncIndicatorSpec] get %v", nn.String())

	spec := sc.indicatorManager.GetIndicatorSpec(nn)
	if spec == nil {
		klog.Warningf("[syncIndicatorSpec] spd %v is nil", nn.String())
		return
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		spd, err := sc.spdLister.ServiceProfileDescriptors(nn.Namespace).Get(nn.Name)
		if err != nil {
			klog.Errorf("[syncIndicatorSpec] failed to get spd [%v], err: %v", nn.String(), err)
			return err
		}

		spdCopy := spd.DeepCopy()
		sc.mergeIndicatorSpec(spdCopy, *spec)
		if apiequality.Semantic.DeepEqual(spd.Spec, spdCopy.Spec) {
			return nil
		}

		if _, err := sc.spdControl.UpdateSPD(sc.ctx, spdCopy, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("[syncIndicatorSpec] failed to update spd for %s: %v", nn.String(), err)
			return err
		}

		_ = sc.metricsEmitter.StoreInt64(metricsNameSyncIndicatorSpec, 1, metrics.MetricTypeNameCount, metrics.MetricTag{
			Key: "status", Val: "success",
		})

		klog.V(4).Infof("[syncIndicatorSpec] successfully updated spd %s to %+v", nn.String(), spdCopy.Spec)
		return nil
	})
	if err != nil {
		// todo if failed to get spd, re-enqueue to update next time
		klog.Errorf("[syncIndicatorSpec] failed to retry on conflict update spd for %s: %v", nn.String(), err)
		_ = sc.metricsEmitter.StoreInt64(metricsNameSyncIndicatorSpec, 1, metrics.MetricTypeNameCount, metrics.MetricTag{
			Key: "status", Val: "failed",
		})
	}
}

func (sc *SPDController) syncIndicatorStatus() {
	c := sc.indicatorManager.GetIndicatorStatusChan()
	for {
		select {
		case nn, ok := <-c:
			if !ok {
				klog.Infof("[syncIndicatorStatus] indicator status chan is closed")
				return
			}

			sc.syncStatus(nn)
			_ = sc.metricsEmitter.StoreInt64(metricsNameIndicatorStatusChanLength, int64(len(c)), metrics.MetricTypeNameRaw)
		case <-sc.ctx.Done():
			klog.Infoln("[syncIndicatorStatus] stop spd vpa status queue worker.")
			return
		}
	}
}

func (sc *SPDController) syncStatus(nn types.NamespacedName) {
	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		klog.V(5).Infof("[spd] finished syncing indicator status %q (%v)", nn, costs)
		_ = sc.metricsEmitter.StoreInt64(metricsNameSyncIndicatorStatusCost, costs.Microseconds(),
			metrics.MetricTypeNameRaw, metrics.MetricTag{Key: "name", Val: nn.String()})
	}()

	klog.V(5).Infof("[syncIndicatorStatus] get %v", nn.String())

	status := sc.indicatorManager.GetIndicatorStatus(nn)
	if status == nil {
		klog.Warningf("[syncIndicatorStatus] spd status %v is nil", nn.String())
		return
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		spd, err := sc.spdLister.ServiceProfileDescriptors(nn.Namespace).Get(nn.Name)
		if err != nil {
			klog.Errorf("[syncIndicatorStatus] failed to get spd [%v], err: %v", nn.String(), err)
			return err
		}

		spdCopy := spd.DeepCopy()
		sc.mergeIndicatorStatus(spdCopy, *status)
		if apiequality.Semantic.DeepEqual(spd.Status, spdCopy.Status) {
			return nil
		}

		if _, err := sc.spdControl.UpdateSPDStatus(sc.ctx, spdCopy, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("[syncIndicatorStatus] failed to update spd status for %s: %v", nn.String(), err)
			return err
		}

		_ = sc.metricsEmitter.StoreInt64(metricsNameSyncIndicatorStatus, 1, metrics.MetricTypeNameCount, metrics.MetricTag{
			Key: "status", Val: "success",
		})

		klog.V(4).Infof("[syncIndicatorStatus] successfully updated spd status %s to %+v", nn.String(), spdCopy.Status)
		return nil
	})
	if err != nil {
		// todo if failed to get spd, re-enqueue to update next time
		klog.Errorf("[syncIndicatorStatus] failed to retry on conflict update spd status for %s: %v", nn.String(), err)
		_ = sc.metricsEmitter.StoreInt64(metricsNameSyncIndicatorStatus, 1, metrics.MetricTypeNameCount, metrics.MetricTag{
			Key: "status", Val: "failed",
		})
	}
}

func (sc *SPDController) mergeIndicatorSpec(spd *apiworkload.ServiceProfileDescriptor, expected apiworkload.ServiceProfileDescriptorSpec) {
	for _, indicator := range expected.BusinessIndicator {
		util.InsertSPDBusinessIndicatorSpec(&spd.Spec, &indicator)
	}
	for _, indicator := range expected.SystemIndicator {
		util.InsertSPDSystemIndicatorSpec(&spd.Spec, &indicator)
	}

	for i := 0; i < len(spd.Spec.BusinessIndicator); i++ {
		if _, ok := sc.indicatorsSpecBusiness[spd.Spec.BusinessIndicator[i].Name]; !ok {
			klog.Infof("skip spec business %v for spd %v", spd.Spec.BusinessIndicator[i].Name, spd.Name)
			spd.Spec.BusinessIndicator = append(spd.Spec.BusinessIndicator[:i], spd.Spec.BusinessIndicator[i+1:]...)
		}
	}

	for i := 0; i < len(spd.Spec.SystemIndicator); i++ {
		if _, ok := sc.indicatorsSpecSystem[spd.Spec.SystemIndicator[i].Name]; !ok {
			klog.Infof("skip spec system %v for spd %v", spd.Spec.SystemIndicator[i].Name, spd.Name)
			spd.Spec.SystemIndicator = append(spd.Spec.SystemIndicator[:i], spd.Spec.SystemIndicator[i+1:]...)
		}
	}
	spd.Spec.BaselinePercent = expected.BaselinePercent
}

func (sc *SPDController) mergeIndicatorStatus(spd *apiworkload.ServiceProfileDescriptor, expected apiworkload.ServiceProfileDescriptorStatus) {
	for _, indicator := range expected.BusinessStatus {
		util.InsertSPDBusinessIndicatorStatus(&spd.Status, &indicator)
	}

	for i := 0; i < len(spd.Status.BusinessStatus); i++ {
		if _, ok := sc.indicatorsStatusBusiness[spd.Status.BusinessStatus[i].Name]; !ok {
			klog.Infof("skip status business %v for spd %v", spd.Status.BusinessStatus[i].Name, spd.Name)
			spd.Status.BusinessStatus = append(spd.Status.BusinessStatus[:i], spd.Status.BusinessStatus[i+1:]...)
		}
	}
}
