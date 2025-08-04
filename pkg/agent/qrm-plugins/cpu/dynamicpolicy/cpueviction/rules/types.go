package rules

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/history"
	v1 "k8s.io/api/core/v1"
)

type CandidatePod struct {
	Pod                   *v1.Pod
	Scores                map[string]int
	TotalScore            int
	WorkloadsEvictionInfo map[string]*WorkloadEvictionInfo
	UsageRatio            float64
}

type WorkloadEvictionInfo struct {
	WorkloadName     string
	StatsByWindow    map[float64]*EvictionStats
	Replicas         int32
	LastEvictionTime int64
	Limit            int
}

type EvictionStats struct {
	EvictionCount int64
	EvictionRatio float64
}

type NumaOverStat struct {
	NumaID         int
	OverloadRatio  float64
	AvgUsageRatio  float64
	MetricsHistory *history.NumaMetricHistory
	Gap            float64
}

// delete
type EvictionRecord struct {
	UID     string
	HasPDB  bool
	Buckets Buckets

	DisruptionsAllowed int32 // 允许中断的数量（currentHealthy - desiredHealthy）
	CurrentHealthy     int32 // 当前健康的 Pod 数量（实际通过健康检查的）
	DesiredHealthy     int32 // 根据规则应保持的最小健康数
	ExpectedPods       int32 // 匹配到的总 Pod 数
}
type Buckets struct {
	List []Bucket `json:"list"`
}

type Bucket struct {
	Time     int64 // 秒级时间戳
	Duration int64
	Count    int64 // 驱逐数量
}
