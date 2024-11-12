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

package metricstore

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type mbReader struct {
	metricsFetcher types.MetricsFetcher
}

func toMBQoSGroup(ccdMetricData map[int]metric.MetricData) *monitor.MBQoSGroup {
	if ccdMetricData == nil {
		return nil
	}

	CCDs := make(sets.Int)
	CCDMBs := make(map[int]*monitor.MBData)
	for ccd, metric := range ccdMetricData {
		CCDs.Insert(ccd)
		CCDMBs[ccd] = &monitor.MBData{
			TotalMB: int(metric.Value),
		}
	}

	result := monitor.MBQoSGroup{
		CCDs:  CCDs,
		CCDMB: nil,
	}

	return &result
}

func (m *mbReader) GetMBQoSGroups() (map[task.QoSGroup]*monitor.MBQoSGroup, error) {
	mbBlob := m.metricsFetcher.GetByStringIndex(consts.MetricTotalMemBandwidthQoSGroup)

	var qosCCDMB map[string]map[int]metric.MetricData
	qosCCDMB, ok := mbBlob.(map[string]map[int]metric.MetricData)
	if !ok {
		return nil, fmt.Errorf("unexpected metric blob by key %s", consts.MetricTotalMemBandwidthQoSGroup)
	}

	result := make(map[task.QoSGroup]*monitor.MBQoSGroup)
	for qos, data := range qosCCDMB {
		result[task.QoSGroup(qos)] = toMBQoSGroup(data)
	}
	return result, nil
}

func NewMBReader(metricsFetcher types.MetricsFetcher) monitor.MBMonitor {
	return &mbReader{
		metricsFetcher: metricsFetcher,
	}
}
