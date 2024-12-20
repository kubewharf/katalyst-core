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
	"github.com/pkg/errors"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type mbReader struct {
	metricsFetcher types.MetricsFetcher
}

func toMBQoSGroup(ccdMetricData map[int][]metric.MetricData) *monitor.MBQoSGroup {
	if ccdMetricData == nil {
		return nil
	}

	CCDs := make(sets.Int)
	CCDMBs := make(map[int]*monitor.MBData)
	for ccd, mbSummry := range ccdMetricData {
		// mbSummry if slice of 2 elements: 0 - total mb; 1: local ratio
		CCDs.Insert(ccd)
		CCDMBs[ccd] = &monitor.MBData{
			TotalMB:      int(mbSummry[0].Value),
			LocalTotalMB: int(mbSummry[0].Value * mbSummry[1].Value),
		}
	}

	result := monitor.MBQoSGroup{
		CCDs:  CCDs,
		CCDMB: CCDMBs,
	}

	return &result
}

func (m *mbReader) getMetricByQoSCCD(metricKey string) (map[string]map[int][]metric.MetricData, error) {
	mbTotalBlob := m.metricsFetcher.GetByStringIndex(metricKey)

	var qosCCDMetrics map[string]map[int][]metric.MetricData
	qosCCDMetrics, ok := mbTotalBlob.(map[string]map[int][]metric.MetricData)
	if !ok {
		return nil, fmt.Errorf("unexpected metric blob by key %s: %T", metricKey, mbTotalBlob)
	}

	return qosCCDMetrics, nil
}

func (m *mbReader) GetMBQoSGroups() (map[qosgroup.QoSGroup]*monitor.MBQoSGroup, error) {
	// retrieve mb total and mb local-remote ratio
	qosCCDMBTotal, err := m.getMetricByQoSCCD(consts.MetricTotalMemBandwidthQoSGroup)
	if err != nil {
		return nil, errors.Wrap(err, "failed to retrieve mb summary from metric store")
	}

	result := make(map[qosgroup.QoSGroup]*monitor.MBQoSGroup)
	for qos, data := range qosCCDMBTotal {
		result[qosgroup.QoSGroup(qos)] = toMBQoSGroup(data)
	}
	return result, nil
}

func NewMBReader(metricsFetcher types.MetricsFetcher) monitor.MBMonitor {
	return &mbReader{
		metricsFetcher: metricsFetcher,
	}
}
