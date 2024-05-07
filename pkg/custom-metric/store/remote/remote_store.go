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

package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	metricconf "github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const MetricStoreNameRemoteMemory = "remote-memory-store"

const (
	metricsNameStoreRemoteGetCostFinish       = "kcmas_get_finish"
	metricsNameStoreRemoteGetCostSendRequests = "kcmas_get_requests"
	metricsNameStoreRemoteGetMetricCount      = "kcmas_get_metric_count"
	metricsNameStoreRemoteGetItemCount        = "kcmas_get_item_count"

	metricsNameStoreRemoteSendRequest = "kcmas_send_request"
)

// RemoteMemoryMetricStore implements MetricStore with multiple-nodes versioned
// in-memory storage, and each shard will be responsible for some splits of the
// total metrics. it will be used when the cluster becomes too large.
//
// RemoteMemoryMetricStore itself will be responsible for shard-splitting logic,
// and it should be a wrapper of LocalMemoryMetricStore to reuse its internalMetric structure.
type RemoteMemoryMetricStore struct {
	ctx         context.Context
	tags        []metrics.MetricTag
	storeConf   *metricconf.StoreConfiguration
	genericConf *metricconf.GenericMetricConfiguration

	client  *http.Client
	emitter metrics.MetricEmitter

	sharding *ShardingController
}

var _ store.MetricStore = &RemoteMemoryMetricStore{}

func NewRemoteMemoryMetricStore(ctx context.Context, baseCtx *katalystbase.GenericContext,
	genericConf *metricconf.GenericMetricConfiguration, storeConf *metricconf.StoreConfiguration,
) (*RemoteMemoryMetricStore, error) {
	client := process.NewDefaultHTTPClient()

	if storeConf.StoreServerReplicaTotal <= 0 {
		return nil, fmt.Errorf("total store server replica must be positive")
	}
	sharding, err := NewShardingController(ctx, baseCtx, storeConf)
	if err != nil {
		return nil, err
	}

	tags := []metrics.MetricTag{
		{Key: "name", Val: MetricStoreNameRemoteMemory},
	}
	return &RemoteMemoryMetricStore{
		ctx:         ctx,
		tags:        tags,
		genericConf: genericConf,
		storeConf:   storeConf,
		client:      client,
		emitter:     baseCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags("remote_store"),
		sharding:    sharding,
	}, nil
}

func (r *RemoteMemoryMetricStore) Name() string { return MetricStoreNameRemoteMemory }

func (r *RemoteMemoryMetricStore) Start() error {
	return r.sharding.Start()
}

func (r *RemoteMemoryMetricStore) Stop() error {
	return r.sharding.Stop()
}

func (r *RemoteMemoryMetricStore) InsertMetric(seriesList []*data.MetricSeries) error {
	start := time.Now()

	contents, err := json.Marshal(seriesList)
	if err != nil {
		return err
	}

	newCtx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
	}()
	requests, err := r.sharding.GetRequests(newCtx, local.ServingSetPath)
	if err != nil {
		return err
	}

	_, wCnt := r.sharding.GetRWCount()
	klog.V(4).Infof("insert need to write %v among %v", wCnt, len(requests))

	success := 0
	var responseLock sync.Mutex
	// insert will always try to write into all store instances instead of write-counts
	err = r.sendRequests(cancel, requests, len(requests), r.tags,
		func(req *http.Request) {
			req.Body = io.NopCloser(bytes.NewReader(contents))
		},
		func(_ io.ReadCloser) error {
			responseLock.Lock()
			success++
			responseLock.Unlock()
			return nil
		},
	)
	if err != nil {
		return err
	}

	defer func() {
		finished := time.Now()
		klog.V(6).Infof("insert cost %v", finished.Sub(start))
	}()

	if success < wCnt {
		return fmt.Errorf("failed to perform quorum write actual %v expect %v", success, wCnt)
	}

	klog.V(4).Infof("successfully set with len %v", len(seriesList))
	return nil
}

func (r *RemoteMemoryMetricStore) GetMetric(_ context.Context, namespace, metricName, objName string, gr *schema.GroupResource,
	objSelector, metricSelector labels.Selector, latest bool,
) ([]types.Metric, error) {
	start := time.Now()
	tags := r.generateMetricsTags(metricName, objName)

	newCtx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
	}()
	requests, err := r.sharding.GetRequests(newCtx, local.ServingGetPath)
	if err != nil {
		return nil, err
	}

	rCnt, _ := r.sharding.GetRWCount()
	klog.Infof("[remote-store] metric %v, obj %v, get need to read %v among %v", metricName, objName, rCnt, len(requests))

	var responseLock sync.Mutex
	var metricLists [][]types.Metric
	err = r.sendRequests(cancel, requests, rCnt, tags,
		func(req *http.Request) {
			values := req.URL.Query()
			if len(namespace) > 0 {
				values.Set(local.StoreGETParamNamespace, namespace)
			}
			if len(metricName) > 0 {
				values.Set(local.StoreGETParamMetricName, metricName)
			}
			if metricSelector != nil && metricSelector.String() != "" {
				values.Set(local.StoreGETParamMetricSelector, metricSelector.String())
			}
			if gr != nil {
				values.Set(local.StoreGETParamObjectGR, gr.String())
			}
			if len(objName) > 0 {
				values.Set(local.StoreGETParamObjectName, objName)
			}
			if objSelector != nil && objSelector.String() != "" {
				values.Set(local.StoreGETParamMObjectSelector, objSelector.String())
			}
			if latest {
				values.Set(local.StoreGETParamLatest, fmt.Sprintf("%v", latest))
			}

			req.URL.RawQuery = values.Encode()
		},
		func(body io.ReadCloser) error {
			metricList, err := types.DecodeMetricList(body, metricName)
			if err != nil {
				return fmt.Errorf("decode err: %v", err)
			}
			responseLock.Lock()
			metricLists = append(metricLists, metricList)
			responseLock.Unlock()
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	defer func() {
		finishCosts := time.Now().Sub(start).Microseconds()
		klog.Infof("[remote-store] get-finish: metric %v, obj %v, costs %v(ms), resultCount %v", metricName, objName, finishCosts, len(metricLists))
		_ = r.emitter.StoreInt64(metricsNameStoreRemoteGetCostFinish, finishCosts, metrics.MetricTypeNameRaw, append(tags,
			metrics.MetricTag{Key: "count", Val: fmt.Sprintf("%v", len(metricLists))})...)
	}()

	finishCosts := time.Now().Sub(start).Microseconds()
	klog.Infof("[remote-store] get-requests: metric %v, obj %v, costs %v(ms)", metricName, objName, finishCosts)
	_ = r.emitter.StoreInt64(metricsNameStoreRemoteGetCostSendRequests, finishCosts, metrics.MetricTypeNameRaw, tags...)

	if len(metricLists) < rCnt {
		return nil, fmt.Errorf("failed to perform quorum read actual %v expect %v", len(metricLists), rCnt)
	}

	res := data.MergeInternalMetricList(metricName, metricLists...)
	itemLen := int64(0)
	for _, r := range res {
		itemLen += int64(r.Len())
	}
	klog.Infof("[remote-store] metric %v, obj %v, successfully get with len %v", metricName, objName, len(res))
	_ = r.emitter.StoreInt64(metricsNameStoreRemoteGetMetricCount, int64(len(res)), metrics.MetricTypeNameRaw, tags...)
	_ = r.emitter.StoreInt64(metricsNameStoreRemoteGetItemCount, itemLen, metrics.MetricTypeNameRaw, tags...)
	return res, nil
}

func (r *RemoteMemoryMetricStore) ListMetricMeta(ctx context.Context, withObject bool) ([]types.MetricMeta, error) {
	start := time.Now()

	newCtx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
	}()
	requests, err := r.sharding.GetRequests(newCtx, local.ServingListPath)
	if err != nil {
		return nil, err
	}

	rCnt, _ := r.sharding.GetRWCount()
	klog.V(6).Infof("list with objects need to read %v among %v", rCnt, len(requests))

	var responseLock sync.Mutex
	var metricMetaLists [][]types.MetricMeta
	err = r.sendRequests(cancel, requests, rCnt, r.tags,
		func(req *http.Request) {
			values := req.URL.Query()
			if withObject {
				values.Set(local.StoreListParamObjected, "true")
			}
			req.URL.RawQuery = values.Encode()
		},
		func(body io.ReadCloser) error {
			metricMetaList, err := types.DecodeMetricMetaList(body)
			if err != nil {
				return fmt.Errorf("decode response err: %v", err)
			}
			responseLock.Lock()
			metricMetaLists = append(metricMetaLists, metricMetaList)
			responseLock.Unlock()
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	defer func() {
		finished := time.Now()
		klog.V(6).Infof("list with objects cost %v", finished.Sub(start))
	}()

	if len(metricMetaLists) < rCnt {
		return nil, fmt.Errorf("failed to perform quorum read actual %v expect %v", len(metricMetaLists), rCnt)
	}

	res := types.PackMetricMetaList(metricMetaLists...)
	klog.V(4).Infof("successfully list with len %v", len(res))
	return res, nil
}

// todo, currently we will not support any timeout configurations for http-requests
func (r *RemoteMemoryMetricStore) sendRequests(cancel func(),
	reqs []*http.Request, readyCnt int, tags []metrics.MetricTag,
	requestWrapF func(req *http.Request), responseWrapF func(body io.ReadCloser) error,
) error {
	if len(reqs) == 0 {
		return nil
	}

	failChan := make(chan error, len(reqs))
	successChan := make(chan struct{}, len(reqs))

	wg := sync.WaitGroup{}
	for i := range reqs {
		wg.Add(1)
		req := reqs[i]

		go func() {
			err := r.sendRequest(req, tags, requestWrapF, responseWrapF)
			if err != nil {
				failChan <- fmt.Errorf("%v send request err: %v", req.URL.String(), err)
			} else {
				successChan <- struct{}{}
			}
			wg.Done()
		}()
	}

	fail, success := 0, 0
	for {
		select {
		case err := <-failChan:
			fail++
			klog.Errorf("failed to send request %v", err)
		case <-successChan:
			success++
		}

		if success+fail >= len(reqs) {
			// always try to cancel all requests before quiting
			cancel()
			klog.Infof("break sending requests, success %v, fail %v, total %v", success, fail, len(reqs))
			break
		}
	}
	// wait for all goroutines to quit, and then close all channels to avoid memory leak
	wg.Wait()
	close(failChan)
	close(successChan)

	if success < readyCnt {
		return fmt.Errorf("failed to get more than %v valid responses", readyCnt)
	}
	return nil
}

// sendRequest works as a uniformed function to construct http requests, as
// well as send this requests to the server side.
func (r *RemoteMemoryMetricStore) sendRequest(req *http.Request, tags []metrics.MetricTag,
	requestWrapFunc func(req *http.Request), responseWrapF func(body io.ReadCloser) error,
) error {
	start := time.Now()
	defer func() {
		finishCosts := time.Now().Sub(start).Microseconds()
		klog.Infof("[remote-store] send-request: url %+v, costs %v(ms)", req.URL, finishCosts)
		_ = r.emitter.StoreInt64(metricsNameStoreRemoteSendRequest, finishCosts, metrics.MetricTypeNameRaw, tags...)
	}()

	requestWrapFunc(req)

	klog.V(6).Infof("sendRequest %v", req.URL)
	resp, err := r.client.Do(req)
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if err != nil {
		return fmt.Errorf("send http requests err: %v", err)
	} else if resp == nil {
		return fmt.Errorf("response err: %v", "respnsonse nil")
	} else if resp.Body == nil {
		return fmt.Errorf("response err: %v", "body is nil")
	} else if resp.StatusCode != http.StatusOK {
		buf := bytes.NewBuffer([]byte{})
		_, _ = io.Copy(buf, resp.Body)
		return fmt.Errorf("response err: status code %v, body: %v", resp.StatusCode, buf.String())
	}

	if err := responseWrapF(resp.Body); err != nil {
		return fmt.Errorf("failed to handle response %v", err)
	}
	return nil
}

// generateMetricsTags returns tags for the corresponding requests
func (r *RemoteMemoryMetricStore) generateMetricsTags(metricName, objName string) []metrics.MetricTag {
	if metricName == "" {
		metricName = "empty"
	}
	if objName == "" {
		objName = "empty"
	}
	return append(r.tags,
		metrics.MetricTag{Key: "metric_name", Val: metricName},
		metrics.MetricTag{Key: "object_name", Val: objName},
	)
}
