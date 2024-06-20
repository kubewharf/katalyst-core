package loadaware

import (
	"github.com/kubewharf/katalyst-core/pkg/controller/npd/indicator-plugin/loadaware/sorter"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	quotav1 "k8s.io/apiserver/pkg/quota/v1"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"time"
)

// getUsage transfer cpu Nano to Milli, memory Ki to Mega
func getUsage(src corev1.ResourceList) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(src.Cpu().MilliValue(), resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(src.Memory().Value(), resource.BinarySI),
	}
}

func calCPUAndMemoryAvg(dataList []corev1.ResourceList) corev1.ResourceList {
	cpuSum := int64(0)
	memorySum := int64(0)
	for _, value := range dataList {
		cpuSum += value.Cpu().MilliValue()
		memorySum += value.Memory().Value()
	}
	avgCPU := cpuSum / int64(len(dataList))
	avgMem := memorySum / int64(len(dataList))
	return corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(avgCPU, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(avgMem, resource.BinarySI),
	}
}

func calCPUAndMemoryMax(dataList []*ResourceListWithTime) corev1.ResourceList {
	maxCPU := int64(0)
	maxMem := int64(0)
	for _, value := range dataList {
		if value.Cpu().MilliValue() > maxCPU {
			maxCPU = value.Cpu().MilliValue()
		}
		if value.Memory().Value() > maxMem {
			maxMem = value.Memory().Value()
		}
	}
	return corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(maxCPU, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(maxMem, resource.BinarySI),
	}
}

func isNodeMetricsExpired(nodeMetric *v1beta1.NodeMetrics, now metav1.Time) bool {
	return nodeMetric.Timestamp.Time.Add(NodeMetricExpiredTime).Before(now.Time)
}

func isPodMetricsExpired(podMetrics *v1beta1.PodMetrics, now metav1.Time) bool {
	return podMetrics.Timestamp.Time.Add(NodeMetricExpiredTime).Before(now.Time)
}

func refreshNodeMetricData(metricData *NodeMetricData, metricInfo *v1beta1.NodeMetrics, now time.Time) {
	metricData.lock.Lock()
	defer metricData.lock.Unlock()
	metricData.LatestUsage = metricInfo.Usage.DeepCopy()
	metricData.Latest15MinCache = append(metricData.Latest15MinCache, getUsage(metricInfo.Usage))
	if len(metricData.Latest15MinCache) > Avg15MinPointNumber {
		metricData.Latest15MinCache = metricData.Latest15MinCache[1:]
	}
	// calculate 5 min avg data
	metaData5min := metricData.Latest15MinCache
	if len(metricData.Latest15MinCache) > Avg5MinPointNumber {
		metaData5min = metricData.Latest15MinCache[len(metricData.Latest15MinCache)-Avg5MinPointNumber:]
	}
	avg5Min := calCPUAndMemoryAvg(metaData5min)
	metricData.Avg5Min = avg5Min.DeepCopy()

	// calculate 15 min avg data
	avg15Min := calCPUAndMemoryAvg(metricData.Latest15MinCache)
	metricData.Avg15Min = avg15Min.DeepCopy()

	// calculate 1 hour max data
	if metricData.ifCanInsertLatest1HourCache(now) {
		resWithTime := &ResourceListWithTime{
			ResourceList: avg15Min.DeepCopy(),
			Ts:           now.Unix(),
		}
		metricData.Latest1HourCache = append(metricData.Latest1HourCache, resWithTime)
		if len(metricData.Latest1HourCache) > Max1HourPointNumber {
			metricData.Latest1HourCache = metricData.Latest1HourCache[1:]
		}
	}
	max1Hour := calCPUAndMemoryMax(metricData.Latest1HourCache)
	metricData.Max1Hour = max1Hour.DeepCopy()

	//calculate 1 day max data
	if metricData.ifCanInsertLatest1DayCache(now) {
		resWithTime := &ResourceListWithTime{
			ResourceList: max1Hour.DeepCopy(),
			Ts:           now.Unix(),
		}
		metricData.Latest1DayCache = append(metricData.Latest1DayCache, resWithTime)
		if len(metricData.Latest1DayCache) > Max1DayPointNumber {
			metricData.Latest1DayCache = metricData.Latest1DayCache[1:]
		}
	}
	max1Day := calCPUAndMemoryMax(metricData.Latest1DayCache)
	metricData.Max1Day = max1Day.DeepCopy()
}

func refreshPodMetricData(metricData *PodMetricData, metricInfo *v1beta1.PodMetrics) {
	metricData.lock.Lock()
	defer metricData.lock.Unlock()
	podUsage := make(corev1.ResourceList)
	for _, containerMetrics := range metricInfo.Containers {
		podUsage = quotav1.Add(podUsage, containerMetrics.Usage)
	}
	metricData.LatestUsage = podUsage.DeepCopy()
	//calculate 5 min avg data
	metricData.Latest5MinCache = append(metricData.Latest5MinCache, getUsage(podUsage))
	if len(metricData.Latest5MinCache) > Avg5MinPointNumber {
		metricData.Latest5MinCache = metricData.Latest5MinCache[len(metricData.Latest5MinCache)-Avg5MinPointNumber:]
	}
	avg5Min := calCPUAndMemoryAvg(metricData.Latest5MinCache)
	metricData.Avg5Min = avg5Min.DeepCopy()
}

func getTopNPodUsages(podUsages map[string]corev1.ResourceList, maxPodUsageCount int) map[string]corev1.ResourceList {
	if len(podUsages) <= maxPodUsageCount {
		return podUsages
	}
	resourceToWeightMap := map[corev1.ResourceName]int64{
		corev1.ResourceCPU:    int64(1),
		corev1.ResourceMemory: int64(1),
	}
	var objs []*sorter.Obj
	totalResUsage := make(corev1.ResourceList)
	for name, usage := range podUsages {
		obj := sorter.Obj{
			Name: name,
		}
		objs = append(objs, &obj)
		totalResUsage = quotav1.Add(totalResUsage, usage)
	}
	sorter.SortPodsByUsage(objs, podUsages, totalResUsage, resourceToWeightMap)
	topNPodUsages := make(map[string]corev1.ResourceList)
	for i, obj := range objs {
		if i >= maxPodUsageCount {
			break
		}
		if podUsage, ok := podUsages[obj.Name]; ok {
			topNPodUsages[obj.Name] = podUsage
		}
	}
	return topNPodUsages
}

func calNodeLoad(resourceName corev1.ResourceName, usage, totalRes corev1.ResourceList) int64 {
	if usage == nil || totalRes == nil {
		return 0
	}
	used := int64(0)
	total := int64(0)
	if resourceName == corev1.ResourceCPU {
		used = usage.Cpu().MilliValue()
		total = totalRes.Cpu().MilliValue()
	} else {
		used = usage.Memory().Value()
		total = totalRes.Memory().Value()
	}
	if total == 0 {
		return 0
	}
	if used >= total {
		return 99
	}
	return used * 100 / total
}
