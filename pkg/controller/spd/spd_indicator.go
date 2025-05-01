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
	"strings"
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
	metricsNameSyncIndicatorSpec                  = "sync_indicator_spec"
	metricsNameSyncIndicatorStatus                = "sync_indicator_status"
	metricsNameSyncIndicatorSpecCost              = "sync_indicator_spec_cost"
	metricsNameSyncIndicatorStatusCost            = "sync_indicator_status_cost"
	metricsNameIndicatorSpecChanLength            = "indicator_spec_chan_length"
	metricsNameIndicatorStatusChanLength          = "indicator_status_chan_length"
	metricsNameCreateSPDByWorkloadCost            = "create_spd_by_workload_cost"
	metricsNameInitializeSPDStatusByWorkloadDelay = "initialize_spd_status_by_workload_delay"
	metricsNameSPDCreatedAfterPod                 = "spd_created_after_pod"
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

		klog.V(4).Infof("[syncIndicatorStatus] successfully updated spd status %s", nn.String())
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
	for _, indicator := range expected.ExtendedIndicator {
		util.InsertSPDExtendedIndicatorSpec(&spd.Spec, &indicator)
	}

	// process BusinessIndicator
	businessNew := spd.Spec.BusinessIndicator[:0]
	for _, indicator := range spd.Spec.BusinessIndicator {
		if shouldKeep(indicator.Name, sc.indicatorsSpecBusiness) {
			businessNew = append(businessNew, indicator)
		} else {
			klog.Infof("skip spec business %v for spd %v", indicator.Name, spd.Name)
		}
	}
	spd.Spec.BusinessIndicator = businessNew

	// process SystemIndicator
	systemNew := spd.Spec.SystemIndicator[:0]
	for _, indicator := range spd.Spec.SystemIndicator {
		if shouldKeep(indicator.Name, sc.indicatorsSpecSystem) {
			systemNew = append(systemNew, indicator)
		} else {
			klog.Infof("skip spec system %v for spd %v", indicator.Name, spd.Name)
		}
	}
	spd.Spec.SystemIndicator = systemNew

	// process ExtendedIndicator
	extendedNew := spd.Spec.ExtendedIndicator[:0]
	for _, indicator := range spd.Spec.ExtendedIndicator {
		if shouldKeep(indicator.Name, sc.indicatorsSpecExtended) {
			extendedNew = append(extendedNew, indicator)
		} else {
			klog.Infof("skip spec extended %v for spd %v", indicator.Name, spd.Name)
		}
	}
	spd.Spec.ExtendedIndicator = extendedNew
}

func (sc *SPDController) mergeIndicatorStatus(spd *apiworkload.ServiceProfileDescriptor, expected apiworkload.ServiceProfileDescriptorStatus) {
	for _, indicator := range expected.BusinessStatus {
		util.InsertSPDBusinessIndicatorStatus(&spd.Status, &indicator)
	}

	BusinessStatusNew := spd.Status.BusinessStatus[:0]
	for _, indicator := range spd.Status.BusinessStatus {
		if shouldKeep(indicator.Name, sc.indicatorsStatusBusiness) {
			BusinessStatusNew = append(BusinessStatusNew, indicator)
		} else {
			klog.Infof("skip status business %v for spd %v", indicator.Name, spd.Name)
		}
	}
	spd.Status.BusinessStatus = BusinessStatusNew

	for _, aggMetrics := range expected.AggMetrics {
		util.InsertSPDAggMetricsStatus(&spd.Status, &aggMetrics)
	}

	for i := 0; i < len(spd.Status.AggMetrics); i++ {
		// todo skip merge metrics when scope is empty(legacy), it will be removed in the future
		if spd.Status.AggMetrics[i].Scope == "" {
			continue
		}

		if _, ok := sc.indicatorsStatusAggMetrics[spd.Status.AggMetrics[i].Scope]; !ok {
			klog.Infof("skip status metrics %v for spd %v", spd.Status.AggMetrics[i].Scope, spd.Name)
			spd.Status.AggMetrics = append(spd.Status.AggMetrics[:i], spd.Status.AggMetrics[i+1:]...)
		}
	}
}

func shouldKeep[K ~string](name K, validMap map[K]interface{}) bool {
	if _, ok := validMap[name]; ok {
		return true
	}
	for key := range validMap {
		if strings.HasPrefix(string(name), string(key)) {
			return true
		}
	}
	return false
}
