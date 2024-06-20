package loadaware

import (
	v1 "k8s.io/api/core/v1"
	"sync"
	"time"
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
	loadAwareMetricName          = "node_load"
	metricTagType                = "type"
	metricTagLevel               = "level"
)

var (
	levels = []string{"0-10", "10-20", "20-30", "30-40", "40-50", "50-60", "60-70", "70-80", "80-90", "90-100"}

	podUsageUnrequiredCount = 0
)

type NodeMetricData struct {
	lock             sync.RWMutex
	LatestUsage      v1.ResourceList
	TotalRes         v1.ResourceList
	Avg5Min          v1.ResourceList
	Avg15Min         v1.ResourceList
	Max1Hour         v1.ResourceList
	Max1Day          v1.ResourceList
	Latest15MinCache []v1.ResourceList       //latest 15 1min_avg_data
	Latest1HourCache []*ResourceListWithTime //latest 4 15min_max_data
	Latest1DayCache  []*ResourceListWithTime //latest 24 1hour_max_data
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
	Latest5MinCache []v1.ResourceList //latest 15 1min_avg_data
}

// ResourceListWithTime ...
type ResourceListWithTime struct {
	v1.ResourceList `json:"R,omitempty"`
	Ts              int64 `json:"T,omitempty"`
}
