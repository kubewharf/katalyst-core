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
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	metricsNameSyncWorkloadCost = "sync_workload_cost"
	metricsNameSyncSPDCost      = "sync_spd_cost"
	metricsNameSPDTotal         = "spd_total"

	metricsNameSyncIndicatorSpec         = "sync_indicator_spec"
	metricsNameSyncIndicatorStatus       = "sync_indicator_status"
	metricsNameSyncIndicatorSpecCost     = "sync_indicator_spec_cost"
	metricsNameSyncIndicatorStatusCost   = "sync_indicator_status_cost"
	metricsNameIndicatorSpecChanLength   = "indicator_spec_chan_length"
	metricsNameIndicatorStatusChanLength = "indicator_status_chan_length"
)

func (sc *SPDController) monitor() {
	spdList, err := sc.spdLister.List(labels.Everything())
	if err != nil {
		return
	}

	_ = sc.metricsEmitter.StoreInt64(metricsNameSPDTotal, int64(len(spdList)), metrics.MetricTypeNameRaw)
	_ = sc.metricsEmitter.StoreInt64(metricsNameIndicatorSpecChanLength, int64(len(sc.indicatorManager.GetIndicatorSpecChan())), metrics.MetricTypeNameRaw)
	_ = sc.metricsEmitter.StoreInt64(metricsNameIndicatorStatusChanLength, int64(len(sc.indicatorManager.GetIndicatorStatusChan())), metrics.MetricTypeNameRaw)
}
