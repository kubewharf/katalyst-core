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

package lifecycle

import (
	"fmt"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	metricsNameCNCTotal   = "cnc_total"
	metricsNameCNRTotal   = "cnr_total"
	metricsNameCNRTainted = "cnr_tainted"

	metricsNameHealthState = "health_state"
)

func (cl *CNCLifecycle) monitor() {
	cncs, err := cl.cncLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list all cnc")
		return
	}

	_ = cl.metricsEmitter.StoreInt64(metricsNameCNCTotal, int64(len(cncs)), metrics.MetricTypeNameRaw)
}

func (cl *CNRLifecycle) monitor() {
	cnrs, err := cl.cnrLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list all cnr")
		return
	}

	cnrTainted := int64(0)
	for _, cnr := range cnrs {
		if util.CNRTaintExists(cnr.Spec.Taints, &noScheduleForReclaimedTasksTaint) {
			cnrTainted++
		}
	}

	_ = cl.metricsEmitter.StoreInt64(metricsNameCNRTainted, cnrTainted, metrics.MetricTypeNameRaw)
	_ = cl.metricsEmitter.StoreInt64(metricsNameCNRTotal, int64(len(cnrs)), metrics.MetricTypeNameRaw)
}

func (ec *EvictionController) monitor() {
	if ec.healthState.Load() != "" {
		_ = ec.metricsEmitter.StoreInt64(metricsNameHealthState, 1, metrics.MetricTypeNameRaw,
			[]metrics.MetricTag{
				{Key: "status", Val: ec.healthState.Load()},
				{Key: "threshold", Val: fmt.Sprintf("%v", ec.unhealthyThreshold)},
			}...)
	}
}
