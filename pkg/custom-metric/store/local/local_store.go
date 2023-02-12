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
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	metricconf "github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const MetricStoreNameLocalMemory = "local-memory-store"

// LocalMemoryMetricStore implements MetricStore with single-node versioned
// in-memory storage, and it will be used as a default implementation, especially
// when the amount of internalMetric or the size of cluster is small.
//
// todo: this implementation may be not efficient noe, so we may need to use more
//  complicated structures in the future, such as indexer/aggregator/sort or so on.
type LocalMemoryMetricStore struct {
	ctx         context.Context
	storeConf   *metricconf.StoreConfiguration
	genericConf *metricconf.GenericMetricConfiguration
	emitter     metrics.MetricEmitter

	// validMetricObject is used to map kubernetes objects to gvk and informer
	validMetricObject map[string]schema.GroupVersionResource
	objectInformer    map[string]cache.GenericLister

	syncedFunc  []cache.InformerSynced
	syncSuccess bool

	cache *data.CachedMetric
}

var _ store.MetricStore = &LocalMemoryMetricStore{}

func NewLocalMemoryMetricStore(ctx context.Context, baseCtx *katalystbase.GenericContext,
	genericConf *metricconf.GenericMetricConfiguration, storeConf *metricconf.StoreConfiguration) (store.MetricStore, error) {
	l := &LocalMemoryMetricStore{
		ctx:               ctx,
		genericConf:       genericConf,
		storeConf:         storeConf,
		cache:             data.NewCachedMetric(),
		validMetricObject: data.GetSupportedMetricObject(),
		objectInformer:    make(map[string]cache.GenericLister),
		emitter:           baseCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags("local_store"),
	}

	for r, gvrSchema := range l.validMetricObject {
		wf := baseCtx.MetaInformerFactory.ForResource(gvrSchema)
		l.objectInformer[r] = wf.Lister()
		l.syncedFunc = append(l.syncedFunc, wf.Informer().HasSynced)
	}

	return l, nil
}

func (l *LocalMemoryMetricStore) Name() string { return MetricStoreNameLocalMemory }

func (l *LocalMemoryMetricStore) Start() error {
	klog.Info("starting local memory store")
	if !cache.WaitForCacheSync(l.ctx.Done(), l.syncedFunc...) {
		return fmt.Errorf("unable to sync caches for %s", MetricStoreNameLocalMemory)
	}
	klog.Info("started local memory store")
	l.syncSuccess = true

	go wait.Until(l.gc, 10*time.Second, l.ctx.Done())
	go wait.Until(l.log, time.Minute*3, l.ctx.Done())
	return nil
}

func (l *LocalMemoryMetricStore) Stop() error {
	return nil
}

func (l *LocalMemoryMetricStore) InsertMetric(seriesList []*data.MetricSeries) error {
	// todo: handle aggregate functions in the future if needed
	for _, series := range seriesList {
		internalData, _ := l.parseMetricSeries(series)

		klog.Infof("insert with %v", internalData.String())
		l.cache.Add(internalData)
	}
	return nil
}

func (l *LocalMemoryMetricStore) GetMetric(_ context.Context, namespace, metricName, objName string, gr *schema.GroupResource,
	objSelector, metricSelector labels.Selector, limited int) ([]*data.InternalMetric, error) {
	var res []*data.InternalMetric
	var internalList []*data.InternalMetric

	// always try to get by metric-name if nominated, otherwise list all internal metrics
	if metricName != "" && metricName != "*" {
		internalList, _ = l.cache.GetMetricWithLimit(metricName, limited)
	} else {
		internalList = l.cache.GetMetricInNamespaceWithLimit(namespace, limited)
	}

	for _, internal := range internalList {
		if metricSelector != nil {
			if valid, err := l.checkInternalMetricMatchedWithMetricInfo(internal, namespace, metricSelector); err != nil {
				klog.Errorf("check %+v metric selector %v err %v", internal.GetName(), metricSelector, err)
			} else if !valid {
				klog.V(6).Infof("%v invalid metricSelector", internal.String())
				continue
			}
		}

		if objName != "" {
			if valid, err := l.checkInternalMetricMatchedWithObject(internal, gr, namespace, objName); err != nil {
				klog.Errorf("check %+v object %v err %v", internal.GetName(), objName, err)
			} else if !valid {
				klog.V(6).Infof("%v invalid object", internal.String())
				continue
			}
		}

		if objSelector != nil {
			if valid, err := l.checkInternalMetricMatchedWithObjectList(internal, gr, namespace, objSelector); err != nil {
				klog.Errorf("check %+v object selector %v err %v", internal.GetName(), objSelector, err)
			} else if !valid {
				klog.V(6).Infof("%v invalid objectSelector", internal.String())
				continue
			}
		}

		res = append(res, internal)
	}
	return res, nil
}

func (l *LocalMemoryMetricStore) ListMetricWithObjects(_ context.Context) ([]*data.InternalMetric, error) {
	var res []*data.InternalMetric

	internalList := l.cache.ListAllMetricWithoutValues()
	for _, internal := range internalList {
		if internal.GetObject() == "" || internal.GetObjectName() == "" {
			continue
		}

		res = append(res, internal)
	}
	return res, nil
}

func (l *LocalMemoryMetricStore) ListMetricWithoutObjects(_ context.Context) ([]*data.InternalMetric, error) {
	var res []*data.InternalMetric

	internalList := l.cache.ListAllMetricWithoutValues()
	for _, internal := range internalList {
		if internal.GetObject() != "" || internal.GetObjectName() != "" {
			continue
		}

		res = append(res, internal)
	}
	return res, nil
}

// gc is used to clean those custom metric internalMetric that has been out-of-date
func (l *LocalMemoryMetricStore) gc() {
	expiredTime := time.Now().Add(-1 * l.genericConf.OutOfDataPeriod)
	l.cache.GC(expiredTime)
}

func (l *LocalMemoryMetricStore) log() {
	names := l.cache.ListAllMetricNames()
	klog.Infof("currently with %v metric: %v", len(names), names)
}

// parseMetricSeries parses the given data.MetricSeries into internalMetric
func (l *LocalMemoryMetricStore) parseMetricSeries(series *data.MetricSeries) (*data.InternalMetric,
	[]data.CustomMetricLabelAggregateFunc) {
	// skip already out-of-dated metric contents
	expiredTime := time.Now().Add(-1 * l.genericConf.OutOfDataPeriod).UnixMilli()

	res := data.NewInternalMetric(series.Name)
	var aggList []data.CustomMetricLabelAggregateFunc

	for key, value := range series.Labels {
		switch data.CustomMetricLabelKey(key) {
		case data.CustomMetricLabelKeyNamespace:
			res.SetNamespace(value)
		case data.CustomMetricLabelKeyObject:
			res.SetObject(value)
		case data.CustomMetricLabelKeyObjectName:
			res.SetObjectName(value)
		default:
			if strings.HasPrefix(key, fmt.Sprintf("%v", data.CustomMetricLabelSelectorPrefixKey)) {
				res.SetLabel(strings.TrimPrefix(key, fmt.Sprintf("%v", data.CustomMetricLabelSelectorPrefixKey)), value)
			}

			if strings.HasPrefix(key, fmt.Sprintf("%v", data.CustomMetricLabelAggregatePrefixKey)) {
				agg := data.CustomMetricLabelAggregateFunc(strings.TrimPrefix(key, fmt.Sprintf("%v", data.CustomMetricLabelAggregatePrefixKey)))
				if _, ok := data.ValidCustomMetricLabelAggregateFuncMap[agg]; ok {
					aggList = append(aggList, agg)
				}
			}
		}
	}

	if res.GetObject() != "" {
		if res.GetObjectName() == "" {
			return &data.InternalMetric{}, aggList
		}

		_, err := l.getObject(res.GetObject(), res.GetNamespace(), res.GetObjectName())
		if err != nil {
			klog.Errorf("invalid objects %v %v/%v: %v", res.GetObject(), res.GetNamespace(), res.GetObjectName(), err)
			return &data.InternalMetric{}, aggList
		}
	}

	for _, m := range series.Series {
		if m.Timestamp < expiredTime {
			continue
		}

		res.AppendMetric(data.NewInternalValue(m.Data, m.Timestamp))
	}

	return res, aggList
}

// checkInternalMetricMatchedWithMetricInfo checks if the internal matches with metric info
// if not, return an error to represent the unmatched reasons
func (l *LocalMemoryMetricStore) checkInternalMetricMatchedWithMetricInfo(internal *data.InternalMetric, namespace string,
	metricSelector labels.Selector) (bool, error) {
	if namespace != "" && namespace != "*" && namespace != internal.GetNamespace() {
		klog.V(5).Infof("%v namespace %v not match metric namespace %v", internal.GetName(), namespace, internal.GetNamespace())
		return false, nil
	}

	if !metricSelector.Matches(labels.Set(internal.GetLabels())) {
		klog.V(5).Infof("%v metricSelector %v not match label %v", internal.GetName(), metricSelector, internal.GetLabels())
		return false, nil
	}

	return true, nil
}

// checkInternalMetricMatchedWithObject checks if the internal matches with kubernetes object
// the kubernetes object should be obtained by namespace/name
// if not, return an error to represent the unmatched reasons
func (l *LocalMemoryMetricStore) checkInternalMetricMatchedWithObject(internal *data.InternalMetric,
	gr *schema.GroupResource, namespace, name string) (bool, error) {
	if gr != nil && gr.String() != internal.GetObject() {
		klog.V(5).Infof("gvr %+v not match with objects %v", gr, internal.GetObject())
		return false, nil
	}

	if internal.GetObjectName() != name {
		klog.V(5).Infof("%v namespace %v not match objectName %v", internal.GetName(), namespace, name)
		return false, nil
	}

	_, err := l.getObject(internal.GetObject(), namespace, name)
	if err != nil {
		return false, err
	}

	return true, nil
}

// checkInternalMetricMatchedWithObject checks if the internal matches with kubernetes object
// the kubernetes object should be obtained by label selector
// if not, return an error to represent the unmatched reasons
func (l *LocalMemoryMetricStore) checkInternalMetricMatchedWithObjectList(internal *data.InternalMetric,
	gr *schema.GroupResource, namespace string, selector labels.Selector) (bool, error) {
	if gr != nil && gr.String() != internal.GetObject() {
		klog.V(5).Infof("gvr %+v not match with objects %v", gr, internal.GetObject())
		return false, nil
	}

	obj, err := l.getObject(internal.GetObject(), namespace, internal.GetObjectName())
	if err != nil {
		return false, err
	}

	workload, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return false, fmt.Errorf("%#v failed to transform into unstructured", obj)
	}

	if !selector.Matches(labels.Set(workload.GetLabels())) {
		klog.V(5).Infof("%v selector %v not match label %v", internal.GetName(), selector, workload.GetLabels())
		return false, nil
	}

	return true, nil
}

func (l *LocalMemoryMetricStore) getObject(gvr, namespace, name string) (runtime.Object, error) {
	if name == "" {
		return nil, fmt.Errorf("name should not be empty")
	}

	lister, ok := l.objectInformer[gvr]
	if !ok {
		return nil, fmt.Errorf("unsupported obejct: %v", gvr)
	}

	if namespace != "" {
		return lister.ByNamespace(namespace).Get(name)
	}
	return lister.Get(name)
}

func (l *LocalMemoryMetricStore) getObjectList(gvr, namespace string, selector labels.Selector) ([]runtime.Object, error) {
	lister, ok := l.objectInformer[gvr]
	if !ok {
		return nil, fmt.Errorf("unsupported obejct: %v", gvr)
	}

	if namespace != "" {
		return lister.ByNamespace(namespace).List(selector)
	}
	return lister.List(selector)
}
