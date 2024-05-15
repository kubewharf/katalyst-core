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

package local

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
)

const (
	ServingListPath = "/store/list"
	ServingGetPath  = "/store/get"
	ServingSetPath  = "/store/set"
)

const (
	StoreListParamObjected = "objected"
)

const (
	StoreGETParamNamespace       = "namespace"
	StoreGETParamMetricName      = "metricName"
	StoreGETParamMetricSelector  = "metricSelector"
	StoreGETParamObjectGR        = "objGR"
	StoreGETParamObjectName      = "objName"
	StoreGETParamMObjectSelector = "objSelector"
	StoreGETParamLatest          = "latest"
)

type MemoryStoreData struct{}

// Serve todo: support to use gzip to reduce the transported data through http
func (l *LocalMemoryMetricStore) Serve(mux *http.ServeMux) {
	klog.Infof("local store add serve handler")

	mux.HandleFunc(ServingListPath, l.handleMetricList)
	mux.HandleFunc(ServingGetPath, l.handleMetricGet)
	mux.HandleFunc(ServingSetPath, l.handleMetricSet)
}

func (l *LocalMemoryMetricStore) handleMetricList(w http.ResponseWriter, r *http.Request) {
	if !l.syncSuccess {
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = fmt.Fprintf(w, "store is in initializing status")
		return
	}

	klog.V(6).Infof("receive list requests")

	if r == nil || r.Method != "GET" || r.URL == nil {
		klog.Errorf("Request must be GET with Query URL")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "Request must be GET with Query URL")
		return
	}

	start := time.Now()

	var (
		err             error
		metricMetalList []types.MetricMeta
	)
	if r.URL.Query() == nil || len(getQueryParam(r, StoreListParamObjected)) == 0 {
		metricMetalList, err = l.ListMetricMeta(context.Background(), false)
	} else {
		metricMetalList, err = l.ListMetricMeta(context.Background(), true)
	}

	if err != nil {
		klog.Errorf("get internal list err: %v", err)
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = fmt.Fprintf(w, "Marshal internal err: %v", err)
		return
	}

	readFinished := time.Now()

	bytes, err := json.Marshal(metricMetalList)
	if err != nil {
		klog.Errorf("marshal internal list err: %v", err)
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = fmt.Fprintf(w, "Marshal internal error: %v", err)
		return
	}

	jsonMarshalFinished := time.Now()

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bytes)

	writeRespFinished := time.Now()

	klog.V(6).Infof("list cost read: %v, json: %v, resp: %v, total %v; len %v",
		readFinished.Sub(start),
		jsonMarshalFinished.Sub(readFinished),
		writeRespFinished.Sub(jsonMarshalFinished),
		writeRespFinished.Sub(start),
		len(metricMetalList))
}

func (l *LocalMemoryMetricStore) handleMetricGet(w http.ResponseWriter, r *http.Request) {
	if !l.syncSuccess {
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = fmt.Fprintf(w, "store is in initializing status")
		return
	}

	klog.V(6).Infof("receive get requests")

	if r == nil || r.Method != "GET" || r.URL == nil || r.URL.Query() == nil {
		klog.Errorf("Request must be GET with Query URL")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "Request must be GET with Query URL")
		return
	}

	start := time.Now()

	// 1. parse parameters from URL Query
	var (
		objGR          *schema.GroupResource
		objSelector    labels.Selector = nil
		metricSelector labels.Selector = nil
		latest         bool
	)

	namespace := getQueryParam(r, StoreGETParamNamespace)

	metricName := getQueryParam(r, StoreGETParamMetricName)
	metricSelectorStr := getQueryParam(r, StoreGETParamMetricSelector)
	if len(metricSelectorStr) > 0 {
		selector, err := labels.Parse(metricSelectorStr)
		if err != nil {
			klog.Errorf("metric selector parsing err: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "Item selector parsing %v err: %v", metricSelectorStr, err)
			return
		}
		metricSelector = selector
	}

	objName := getQueryParam(r, StoreGETParamObjectName)
	objGRStr := getQueryParam(r, StoreGETParamObjectGR)
	if len(objGRStr) > 0 {
		_, gr := schema.ParseResourceArg(objGRStr)
		objGR = &gr
	}
	objSelectorStr := getQueryParam(r, StoreGETParamMObjectSelector)
	if len(objSelectorStr) > 0 {
		selector, err := labels.Parse(objSelectorStr)
		if err != nil {
			klog.Errorf("object selector parsing err: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "Object selector parsing %v err: %v", metricSelectorStr, err)
			return
		}
		objSelector = selector
	}

	latestStr := getQueryParam(r, StoreGETParamLatest)
	if len(latestStr) > 0 {
		var err error
		latest, err = strconv.ParseBool(latestStr)
		if err != nil {
			klog.Errorf("limited parsing %v err: %v", latestStr, err)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "Limited parsing %v err %v", latestStr, err)
			return
		}
	}

	readFinished := time.Now()

	// 2. get from local cache and unmarshal into writer
	internalList, err := l.GetMetric(context.Background(), namespace, metricName, objName, objGR, objSelector, metricSelector, latest)
	if err != nil {
		klog.Errorf("get internal list err: %v", err)
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = fmt.Fprintf(w, "Marshal internal err: %v", err)
		return
	}

	bytes, err := json.Marshal(internalList)
	if err != nil {
		klog.Errorf("marshal internal list err: %v", err)
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = fmt.Fprintf(w, "Marshal internal err: %v", err)
		return
	}

	jsonMarshalFinished := time.Now()

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bytes)

	writeRespFinished := time.Now()

	klog.Infof("get metric %v, obj %v, cost read: %v, json: %v, resp: %v, total %v; len %v",
		metricName, objName,
		readFinished.Sub(start),
		jsonMarshalFinished.Sub(readFinished),
		writeRespFinished.Sub(jsonMarshalFinished),
		writeRespFinished.Sub(start),
		len(internalList))
}

func (l *LocalMemoryMetricStore) handleMetricSet(w http.ResponseWriter, r *http.Request) {
	if !l.syncSuccess {
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = fmt.Fprintf(w, "store is in initializing status")
		return
	}

	klog.V(6).Infof("receive set requests")

	if r == nil || r.Method != "POST" || r.Body == nil {
		klog.Errorf("Request must be POST with Body")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "Request must be POST with Body")
		return
	}

	start := time.Now()

	defer func() {
		_ = r.Body.Close()
	}()
	var seriesList []*data.MetricSeries
	if err := json.NewDecoder(r.Body).Decode(&seriesList); err != nil {
		klog.Errorf("read body err: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "Read body err: %v", err)
		return
	}

	jsonMarshalFinished := time.Now()

	if err := l.InsertMetric(seriesList); err != nil {
		klog.Errorf("insert seriesList err: %v", err)
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = fmt.Fprintf(w, "Insert seriesList err: %v", err)
		return
	}

	writeRespFinished := time.Now()

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))
	klog.V(6).Infof("set cost read&json: %v, resp: %v, total %v",
		jsonMarshalFinished.Sub(start),
		writeRespFinished.Sub(jsonMarshalFinished),
		writeRespFinished.Sub(start))
}

// getQueryParam is a common util function to trim parameters from http query;
// if we need to perform string trim or anything like that, do it here
func getQueryParam(r *http.Request, key string) string {
	return strings.TrimSpace(r.URL.Query().Get(key))
}
