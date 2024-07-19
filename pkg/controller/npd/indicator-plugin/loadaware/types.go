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

package loadaware

import (
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
)

const (
	Avg5MinPointNumber  = 5
	Avg15MinPointNumber = 15
	Max1HourPointNumber = 4
	Max1DayPointNumber  = 24

	NodeMetricExpiredTime = 3 * time.Minute
	TransferToCRStoreTime = 5 * time.Minute
)

const (
	LoadAwarePluginName          = "loadAware"
	loadAwareMetricsScope        = "loadAware"
	loadAwareMetricMetadataScope = "loadAware_metadata"
)

var podUsageUnrequiredCount = 0

type NodeMetricData struct {
	lock             sync.RWMutex
	LatestUsage      v1.ResourceList
	TotalRes         v1.ResourceList
	Avg5Min          v1.ResourceList
	Avg15Min         v1.ResourceList
	Max1Hour         v1.ResourceList
	Max1Day          v1.ResourceList
	Latest15MinCache []v1.ResourceList       // latest 15 1min_avg_data
	Latest1HourCache []*ResourceListWithTime // latest 4 15min_max_data
	Latest1DayCache  []*ResourceListWithTime // latest 24 1hour_max_data
}

func (md *NodeMetricData) ifCanInsertLatest1HourCache(now time.Time) bool {
	if len(md.Latest1HourCache) == 0 {
		return true
	}
	latestData := md.Latest1HourCache[len(md.Latest1HourCache)-1]
	lastTime := time.Unix(latestData.Ts, 0)
	if now.After(lastTime.Add(15*time.Minute)) || now.Equal(lastTime.Add(15*time.Minute)) {
		return true
	}
	return false
}

func (md *NodeMetricData) ifCanInsertLatest1DayCache(now time.Time) bool {
	if len(md.Latest1DayCache) == 0 {
		return true
	}
	latestData := md.Latest1DayCache[len(md.Latest1DayCache)-1]
	lastTime := time.Unix(latestData.Ts, 0)
	if now.After(lastTime.Add(1*time.Hour)) || now.Equal(lastTime.Add(1*time.Hour)) {
		return true
	}
	return false
}

type PodMetricData struct {
	lock            sync.RWMutex
	LatestUsage     v1.ResourceList
	Avg5Min         v1.ResourceList
	Latest5MinCache []v1.ResourceList // latest 15 1min_avg_data
}

// ResourceListWithTime ...
type ResourceListWithTime struct {
	v1.ResourceList `json:"R,omitempty"`
	Ts              int64 `json:"T,omitempty"`
}

type ResourceListWithTimeList []*ResourceListWithTime

func (r ResourceListWithTimeList) Len() int {
	return len(r)
}

func (r ResourceListWithTimeList) Swap(i, j int) {
	r[i], r[j] = r[j], r[i]
}

func (r ResourceListWithTimeList) Less(i, j int) bool {
	return r[i].Ts < r[j].Ts
}

func (r ResourceListWithTimeList) ToResourceList() []v1.ResourceList {
	res := make([]v1.ResourceList, 0)
	for i := range r {
		res = append(res, r[i].ResourceList)
	}
	return res
}
