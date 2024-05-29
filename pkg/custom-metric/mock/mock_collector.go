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

package mock

import (
	"context"
	"hash/fnv"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"go.uber.org/atomic"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/collector"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const MetricCollectorNameMock = "mock-collector"

const (
	metricNamePromCollectorSyncCosts = "kcmas_collector_sync_costs"

	metricNamePromCollectorStoreReqCount = "kcmas_collector_store_req_cnt"
	metricNamePromCollectorStoreLatency  = "kcmas_collector_store_latency"
)

// MockCollector produces mock data for test.
type MockCollector struct {
	ctx               context.Context
	collectConf       *metric.CollectorConfiguration
	genericConf       *metric.GenericMetricConfiguration
	podList           []*v1.Pod
	metricNames       []string
	metricBuffer      map[int]*metricBucket
	bufferBucketSize  int
	generateBatchSize int

	metricStore store.MetricStore
	emitter     metrics.MetricEmitter
}

func NewMockCollector(ctx context.Context, baseCtx *katalystbase.GenericContext, genericConf *metric.GenericMetricConfiguration,
	collectConf *metric.CollectorConfiguration, mockConf *metric.MockConfiguration, metricStore store.MetricStore,
) (collector.MetricCollector, error) {
	bufferBucketSize := 2000
	metricBuffer := make(map[int]*metricBucket, bufferBucketSize)
	for i := 0; i < bufferBucketSize; i++ {
		bucketID := i
		metricBuffer[bucketID] = newMetricBucket(bucketID)
	}

	return &MockCollector{
		ctx:         ctx,
		collectConf: collectConf,
		genericConf: genericConf,
		podList:     GenerateMockPods(mockConf.NamespaceCount, mockConf.WorkloadCount, mockConf.PodCount),
		metricNames: []string{
			"pod_memory_rss",
			"pod_memory_usage",
			"pod_cpu_load_1min",
			"pod_cpu_usage",
		},
		metricBuffer:      metricBuffer,
		bufferBucketSize:  bufferBucketSize,
		generateBatchSize: 10,
		metricStore:       metricStore,
		emitter:           baseCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags("mock_collector"),
	}, nil
}

func (m *MockCollector) Name() string { return MetricCollectorNameMock }

func (m *MockCollector) Start() error {
	go wait.Until(m.sync, m.collectConf.SyncInterval, m.ctx.Done())

	for i := 0; i < m.generateBatchSize; i++ {
		batchID := i
		generate := func() {
			m.generateData(batchID)
		}
		time.Sleep(1 * time.Second)
		go wait.Until(generate, 10*time.Second, m.ctx.Done())
	}
	return nil
}

func (m *MockCollector) Stop() error {
	return nil
}

func (m *MockCollector) sync() {
	syncStart := time.Now()
	defer func() {
		costs := time.Since(syncStart)
		general.Infof("mock collector handled with total %v requests, cost %s", len(m.metricBuffer), costs.String())
		_ = m.emitter.StoreInt64(metricNamePromCollectorSyncCosts, costs.Microseconds(), metrics.MetricTypeNameRaw)
	}()

	if len(m.metricBuffer) == 0 {
		return
	}

	var (
		successReqs = atomic.NewInt64(0)
		failedReqs  = atomic.NewInt64(0)
	)

	upload := func(bucketID int) {
		storeStart := time.Now()
		defer func() {
			_ = m.emitter.StoreInt64(metricNamePromCollectorStoreLatency, time.Since(storeStart).Microseconds(), metrics.MetricTypeNameRaw)
		}()

		if m.metricBuffer[bucketID] == nil || m.metricBuffer[bucketID].size() == 0 {
			return
		}

		metricList := m.metricBuffer[bucketID].extractMetrics()

		if err := m.metricStore.InsertMetric(metricList); err != nil {
			failedReqs.Inc()
			return
		}

		successReqs.Inc()
		m.metricBuffer[bucketID].clear()
	}

	workqueue.ParallelizeUntil(m.ctx, 64, len(m.metricBuffer), upload)
	general.Infof("mock collector handle %v succeeded requests, %v failed requests", successReqs.Load(), failedReqs.Load())
	_ = m.emitter.StoreInt64(metricNamePromCollectorStoreReqCount, successReqs.Load(), metrics.MetricTypeNameCount, []metrics.MetricTag{
		{Key: "type", Val: "succeeded"},
	}...)
	_ = m.emitter.StoreInt64(metricNamePromCollectorStoreReqCount, failedReqs.Load(), metrics.MetricTypeNameCount, []metrics.MetricTag{
		{Key: "type", Val: "failed"},
	}...)
}

func formatPodName(pod *v1.Pod) string {
	return pod.Namespace + ":" + pod.Name
}

func (m *MockCollector) generateData(batchID int) {
	general.Infof("start generate mock data for batch %v", batchID)

	start := time.Now()
	defer func() {
		general.Infof("generate mock data for batch %v costs:%v", batchID, time.Now().Sub(start))
	}()

	for _, pod := range m.podList {
		now := time.Now()
		hash := fnv.New64()
		namespacedName := formatPodName(pod)
		hash.Write([]byte(namespacedName))
		bucketID := int(hash.Sum64() % uint64(m.bufferBucketSize))

		if bucketID%m.generateBatchSize != batchID {
			continue
		}

		for _, metricName := range m.metricNames {
			m.metricBuffer[bucketID].insert(pod, metricName, rand.Float64(), now.UnixMilli())
		}
	}
}

// metricBucket is a map key by namespaced pod name
type metricBucket struct {
	id     int
	bucket map[string]*data.MetricSeries
	sync.Mutex
}

func newMetricBucket(id int) *metricBucket {
	return &metricBucket{
		id:     id,
		bucket: make(map[string]*data.MetricSeries),
	}
}

func (m *metricBucket) insert(pod *v1.Pod, metricName string, value float64, timestamp int64) {
	m.Lock()
	defer m.Unlock()

	namespacedName := formatPodName(pod)
	objectMetricKey := namespacedName + ":" + metricName
	ms, msExists := m.bucket[objectMetricKey]
	if !msExists {
		ms = &data.MetricSeries{
			Name: metricName,
			Labels: map[string]string{
				string(data.CustomMetricLabelKeyNamespace):                    pod.Namespace,
				string(data.CustomMetricLabelKeyObject):                       "pods",
				string(data.CustomMetricLabelKeyObjectName):                   pod.Name,
				string(data.CustomMetricLabelSelectorPrefixKey + "node_name"): strconv.Itoa(m.id),
			},
			Series: []*data.MetricData{},
		}
		m.bucket[objectMetricKey] = ms
	}

	ms.Series = append(ms.Series, &data.MetricData{
		Data:      value,
		Timestamp: timestamp,
	})
}

func (m *metricBucket) clear() {
	m.Lock()
	defer m.Unlock()

	m.bucket = make(map[string]*data.MetricSeries)
}

func (m *metricBucket) size() int {
	return len(m.bucket)
}

func (m *metricBucket) extractMetrics() []*data.MetricSeries {
	m.Lock()
	defer m.Unlock()

	metricList := make([]*data.MetricSeries, 0, len(m.bucket))
	for key := range m.bucket {
		metricList = append(metricList, m.bucket[key])
	}
	return metricList
}
