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
	"io/ioutil"
	"k8s.io/klog/v2"
	"net/http"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	metricconf "github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const MetricStoreNameRemoteMemory = "remote-memory-store"

// RemoteMemoryMetricStore implements MetricStore with multiple-nodes versioned
// in-memory storage, and each shard will be responsible for some splits of the
// total metrics. it will be used when the cluster becomes too large.
//
// RemoteMemoryMetricStore itself will be responsible for shard-splitting logic,
// and it should be a wrapper of LocalMemoryMetricStore to reuse its internalMetric structure.
type RemoteMemoryMetricStore struct {
	ctx         context.Context
	storeConf   *metricconf.StoreConfiguration
	genericConf *metricconf.GenericMetricConfiguration

	client  *http.Client
	emitter metrics.MetricEmitter

	sharding *ShardingController
}

var _ store.MetricStore = &RemoteMemoryMetricStore{}

func NewRemoteMemoryMetricStore(ctx context.Context, baseCtx *katalystbase.GenericContext,
	genericConf *metricconf.GenericMetricConfiguration, storeConf *metricconf.StoreConfiguration) (*RemoteMemoryMetricStore, error) {
	client := process.NewDefaultHTTPClient()

	if storeConf.StoreServerReplicaTotal <= 0 {
		return nil, fmt.Errorf("total store server replica must be positive")
	}

	return &RemoteMemoryMetricStore{
		ctx:         ctx,
		genericConf: genericConf,
		storeConf:   storeConf,
		client:      client,
		emitter:     baseCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags("remote_store"),
		sharding:    NewShardingController(ctx, baseCtx, storeConf.StoreServerSelector, storeConf.StoreServerReplicaTotal),
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

	requests := r.sharding.GetRequests(local.ServingSetPath)
	_, wCnt := r.sharding.GetRWCount()
	klog.V(4).Infof("insert need to write %v among %v", wCnt, len(requests))

	bodyList, err := r.sendRequests(context.Background(), requests, func(req *http.Request) {
		req.Body = ioutil.NopCloser(bytes.NewReader(contents))
	})
	if err != nil {
		return err
	}

	defer func() {
		for _, body := range bodyList {
			_ = body.Close()
		}

		finished := time.Now()
		klog.V(6).Infof("insert cost %v", finished.Sub(start))
	}()

	if len(bodyList) < wCnt {
		return fmt.Errorf("failed to perform quorum write actual %v expect %v", len(bodyList), wCnt)
	}

	klog.V(4).Infof("successfully set with len %v", len(seriesList))
	return nil
}

func (r *RemoteMemoryMetricStore) GetMetric(ctx context.Context, namespace, metricName, objName string, gr *schema.GroupResource,
	objSelector, metricSelector labels.Selector, limited int) ([]*data.InternalMetric, error) {
	start := time.Now()

	requests := r.sharding.GetRequests(local.ServingGetPath)
	rCnt, _ := r.sharding.GetRWCount()
	klog.V(6).Infof("get need to read %v among %v", rCnt, len(requests))

	bodyList, err := r.sendRequests(ctx, requests, func(req *http.Request) {
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
		if limited > 0 {
			values.Set(local.StoreGETParamLimited, fmt.Sprintf("%d", limited))
		}

		req.URL.RawQuery = values.Encode()
	})
	if err != nil {
		return nil, err
	}

	defer func() {
		for _, body := range bodyList {
			_ = body.Close()
		}

		finished := time.Now()
		klog.Infof("get cost %v", finished.Sub(start))
	}()

	if len(bodyList) < rCnt {
		return nil, fmt.Errorf("failed to perform quorum read actual %v expect %v", len(bodyList), rCnt)
	}

	var internalLists [][]*data.InternalMetric
	for _, body := range bodyList[0:rCnt] {
		var internalList []*data.InternalMetric
		if err := json.NewDecoder(body).Decode(&internalList); err != nil {
			return nil, fmt.Errorf("decode response err: %v", err)
		}

		internalLists = append(internalLists, internalList)
	}

	res := data.MergeInternalMetricList(internalLists...)
	klog.V(4).Infof("successfully get with len %v", len(res))
	return res, nil
}

func (r *RemoteMemoryMetricStore) ListMetricMeta(ctx context.Context, withObject bool) ([]data.MetricMeta, error) {
	start := time.Now()

	requests := r.sharding.GetRequests(local.ServingListPath)
	rCnt, _ := r.sharding.GetRWCount()
	klog.V(6).Infof("list with objects need to read %v among %v", rCnt, len(requests))

	bodyList, err := r.sendRequests(ctx, requests, func(req *http.Request) {
		values := req.URL.Query()
		if withObject {
			values.Set(local.StoreListParamObjected, "true")
		}
		req.URL.RawQuery = values.Encode()
	})
	if err != nil {
		return nil, err
	}

	defer func() {
		for _, body := range bodyList {
			_ = body.Close()
		}

		finished := time.Now()
		klog.V(6).Infof("list with objects cost %v", finished.Sub(start))
	}()

	if len(bodyList) < rCnt {
		return nil, fmt.Errorf("failed to perform quorum read actual %v expect %v", len(bodyList), rCnt)
	}

	var metricMetaLists [][]data.MetricMeta
	for _, body := range bodyList[0:rCnt] {
		var metricMetaList []data.MetricMeta
		if err := json.NewDecoder(body).Decode(&metricMetaList); err != nil {
			return nil, fmt.Errorf("decode response err: %v", err)
		}

		metricMetaLists = append(metricMetaLists, metricMetaList)
	}

	res := data.MergeMetricMetaList(metricMetaLists...)
	klog.V(4).Infof("successfully list with len %v", len(res))
	return res, nil
}

func (r *RemoteMemoryMetricStore) sendRequests(ctx context.Context, reqs []*http.Request, wrapFunc func(req *http.Request)) ([]io.ReadCloser, error) {
	var bodyList []io.ReadCloser
	var lock sync.Mutex

	wg := sync.WaitGroup{}
	for i := range reqs {

		wg.Add(1)
		req := reqs[i]

		go func() {
			lock.Lock()
			defer lock.Unlock()
			defer wg.Done()

			body, err := r.sendRequest(ctx, req, wrapFunc)
			if err != nil {
				klog.Errorf("failed to send request for %+v: %v", req.URL, err)
				return
			}

			bodyList = append(bodyList, body)
		}()
	}
	wg.Wait()

	return bodyList, nil
}

// sendRequest works as a uniformed function to construct http requests, as
// well as send this requests to the server side.
func (r *RemoteMemoryMetricStore) sendRequest(ctx context.Context, req *http.Request, wrapFunc func(req *http.Request)) (io.ReadCloser, error) {
	wrapFunc(req)

	klog.V(6).Infof("sendRequest %v", req.URL)
	resp, err := r.client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("send http requests err: %v", err)
	}

	if resp == nil {
		return nil, fmt.Errorf("response err: %v", "respnsonse nil")
	} else if resp.Body == nil {
		return nil, fmt.Errorf("response err: %v", "body is nil")
	} else if resp.StatusCode != http.StatusOK {
		defer func() {
			_ = resp.Body.Close()
		}()

		buf := bytes.NewBuffer([]byte{})
		_, _ = io.Copy(buf, resp.Body)
		return nil, fmt.Errorf("response err: status code %v, body: %v", resp.StatusCode, buf.String())
	}

	return resp.Body, nil
}
