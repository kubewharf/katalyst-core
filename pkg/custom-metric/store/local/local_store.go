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

	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	metricconf "github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	MetricStoreNameLocalMemory = "local-memory-store"

	labelIndexEmpty string = "empty"

	MetricNameInsertFailed = "kcmas_local_store_insert_failed"
)

// getLabelIndexFunc is a function to get a index function that indexes based on object's label[labelName]
var getLabelIndexFunc = func(labelName string) func(interface{}) ([]string, error) {
	return func(obj interface{}) ([]string, error) {
		objectMeta, err := meta.Accessor(obj)
		if err != nil {
			return []string{""}, fmt.Errorf("object has no meta: %v", err)
		}
		objectLabels := objectMeta.GetLabels()
		if objectLabels == nil {
			return []string{labelIndexEmpty}, nil
		}
		value, ok := objectLabels[labelName]
		if !ok {
			return []string{labelIndexEmpty}, nil
		}
		return []string{value}, nil
	}
}

// LocalMemoryMetricStore implements MetricStore with single-node versioned
// in-memory storage, and it will be used as a default implementation, especially
// when the amount of internalMetric or the size of cluster is small.
type LocalMemoryMetricStore struct {
	ctx         context.Context
	storeConf   *metricconf.StoreConfiguration
	genericConf *metricconf.GenericMetricConfiguration
	emitter     metrics.MetricEmitter

	// validMetricObject is used to map kubernetes objects to gvk and informer
	validMetricObject map[string]schema.GroupVersionResource
	objectLister      map[string]cache.GenericLister
	objectInformer    map[string]cache.SharedIndexInformer
	indexLabelKeys    []string

	syncedFunc  []cache.InformerSynced
	syncSuccess bool

	cache *data.CachedMetric
}

var _ store.MetricStore = &LocalMemoryMetricStore{}

func NewLocalMemoryMetricStore(ctx context.Context, baseCtx *katalystbase.GenericContext,
	genericConf *metricconf.GenericMetricConfiguration, storeConf *metricconf.StoreConfiguration) (store.MetricStore, error) {
	metricsEmitter := baseCtx.EmitterPool.GetDefaultMetricsEmitter()
	if metricsEmitter == nil {
		metricsEmitter = metrics.DummyMetrics{}
	}

	l := &LocalMemoryMetricStore{
		ctx:               ctx,
		genericConf:       genericConf,
		storeConf:         storeConf,
		cache:             data.NewCachedMetric(metricsEmitter, data.ObjectMetricStoreTypeBucket),
		validMetricObject: data.GetSupportedMetricObject(),
		objectLister:      make(map[string]cache.GenericLister),
		objectInformer:    make(map[string]cache.SharedIndexInformer),
		indexLabelKeys:    storeConf.IndexLabelKeys,
		emitter:           baseCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags("local_store"),
	}

	for r, gvrSchema := range l.validMetricObject {
		wf := baseCtx.MetaInformerFactory.ForResource(gvrSchema)
		l.objectLister[r] = wf.Lister()
		l.objectInformer[r] = wf.Informer()
		if len(storeConf.IndexLabelKeys) > 0 {
			for i := range storeConf.IndexLabelKeys {
				key := storeConf.IndexLabelKeys[i]
				if _, ok := wf.Informer().GetIndexer().GetIndexers()[key]; !ok {
					if err := wf.Informer().AddIndexers(cache.Indexers{key: getLabelIndexFunc(key)}); err != nil {
						klog.Errorf("create label indexer failed, indexName: %v, indexKey: %v, err: %v", storeConf.IndexLabelKeys, storeConf.IndexLabelKeys, err)
						return nil, err
					}
				}
			}
		}
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

	go wait.Until(l.gc, l.storeConf.GCPeriod, l.ctx.Done())
	go wait.Until(l.monitor, time.Minute*3, l.ctx.Done())
	go wait.Until(l.purge, l.storeConf.PurgePeriod, l.ctx.Done())
	return nil
}

func (l *LocalMemoryMetricStore) Stop() error {
	return nil
}

func (l *LocalMemoryMetricStore) InsertMetric(seriesList []*data.MetricSeries) error {
	begin := time.Now()
	defer func() {
		klog.V(5).Infof("[LocalMemoryMetricStore] InsertMetric costs %s", time.Since(begin).String())
	}()

	for _, series := range seriesList {
		begin := time.Now()
		seriesData, ok := l.parseMetricSeries(series)
		if !ok {
			continue
		}

		err := l.cache.AddSeriesMetric(seriesData)
		if err != nil {
			klog.Infof("Insert Metric failed, metricName: %v,objectName:%v,len:%v,", seriesData.GetName(), seriesData.GetObjectName(), seriesData.Len())
			_ = l.emitter.StoreInt64(MetricNameInsertFailed, 1, metrics.MetricTypeNameCount,
				metrics.MetricTag{Key: "metric_name", Val: seriesData.GetName()},
				metrics.MetricTag{Key: "object_kind", Val: seriesData.GetObjectKind()},
			)
			return err
		}
		klog.V(6).Infof("LocalMemoryMetricStore] insert with %v, costs %s", seriesData.String(), time.Since(begin).String())
	}
	return nil
}

func (l *LocalMemoryMetricStore) getObjectMetaByIndex(gr *schema.GroupResource, objSelector labels.Selector) (bool, []types.ObjectMetaImp, error) {
	hitIndex := false
	matchedObjectMeta := make([]types.ObjectMetaImp, 0)

	if objSelector == nil {
		return false, matchedObjectMeta, nil
	}

	requirements, _ := objSelector.Requirements()
	for _, requirement := range requirements {
		if hitIndex {
			break
		}

		for i := range l.indexLabelKeys {
			key := l.indexLabelKeys[i]
			if requirement.Key() == key {
				switch requirement.Operator() {
				case selection.Equals, selection.DoubleEquals, selection.In:
					hitIndex = true
					for indexValue := range requirement.Values() {
						objects, err := l.objectInformer[gr.String()].GetIndexer().ByIndex(key, indexValue)
						if err != nil {
							return false, matchedObjectMeta, fmt.Errorf("get object by index failed,err:%v", err)
						}
						for i := range objects {
							obj := objects[i]
							metadata, ok := obj.(*v1.PartialObjectMetadata)
							if !ok {
								return false, matchedObjectMeta, fmt.Errorf("%#v failed to transform into PartialObjectMetadata", obj)
							}

							matchedObjectMeta = append(matchedObjectMeta, types.ObjectMetaImp{
								ObjectNamespace: metadata.Namespace,
								ObjectName:      metadata.Name,
							})
						}
					}
				}
			}
			if hitIndex {
				return hitIndex, matchedObjectMeta, nil
			}
		}
	}

	return hitIndex, matchedObjectMeta, nil
}

func (l *LocalMemoryMetricStore) GetMetric(_ context.Context, namespace, metricName, objName string, gr *schema.GroupResource,
	objSelector, metricSelector labels.Selector, latest bool) ([]types.Metric, error) {
	begin := time.Now()
	defer func() {
		klog.V(5).Infof("[LocalMemoryMetricStore] GetMetric costs %s", time.Since(begin).String())
	}()

	var (
		res               []types.Metric
		metricList        []types.Metric
		err               error
		hitIndex          bool
		matchedObjectMeta []types.ObjectMetaImp
	)

	// always try to get by metric-name if nominated, otherwise list all internal metrics
	if metricName != "" && metricName != "*" {
		if objName != "" && objName != "*" {
			metricList, _, err = l.cache.GetMetric(namespace, metricName, objName, nil, false, gr, metricSelector, latest)
		} else {
			hitIndex, matchedObjectMeta, err = l.getObjectMetaByIndex(gr, objSelector)
			if err != nil {
				return metricList, err
			}

			if hitIndex {
				metricList, _, err = l.cache.GetMetric(namespace, metricName, objName, matchedObjectMeta, true, gr, metricSelector, latest)
			} else {
				metricList, _, err = l.cache.GetMetric(namespace, metricName, objName, nil, false, gr, metricSelector, latest)
			}
		}
	} else {
		metricList = l.cache.GetAllMetricsInNamespace(namespace)
	}

	if err != nil {
		return metricList, err
	}

	for _, metricItem := range metricList {
		if objName != "" {
			if valid, err := l.checkInternalMetricMatchedWithObject(metricItem, gr, namespace, objName); err != nil {
				klog.Errorf("check %+v object %v err %v", metricItem.GetName(), objName, err)
			} else if !valid {
				klog.V(6).Infof("%v invalid object", metricItem.String())
				continue
			}
		}

		if objSelector != nil {
			if valid, err := l.checkInternalMetricMatchedWithObjectList(metricItem, gr, namespace, objSelector); err != nil {
				klog.Errorf("check %+v object selector %v err %v", metricItem.GetName(), objSelector, err)
			} else if !valid {
				klog.V(6).Infof("%v invalid objectSelector", metricItem.String())
				continue
			}
		}

		res = append(res, metricItem)
	}
	return res, nil
}

func (l *LocalMemoryMetricStore) ListMetricMeta(_ context.Context, withObject bool) ([]types.MetricMeta, error) {
	begin := time.Now()
	defer func() {
		klog.V(5).Infof("[LocalMemoryMetricStore] ListMetricMeta costs %s", time.Since(begin).String())
	}()

	return l.cache.ListAllMetricMeta(withObject), nil
}

// gc is used to clean those custom metric internalMetric that has been out-of-date
func (l *LocalMemoryMetricStore) gc() {
	begin := time.Now()
	defer func() {
		klog.Infof("[LocalMemoryMetricStore] gc costs %s", time.Since(begin).String())
	}()

	expiredTime := begin.Add(-1 * l.genericConf.OutOfDataPeriod)
	l.cache.GC(expiredTime)
}

func (l *LocalMemoryMetricStore) purge() {
	begin := time.Now()
	defer func() {
		klog.Infof("[LocalMemoryMetricStore] purge costs %s", time.Since(begin).String())
	}()

	l.cache.Purge()
}

func (l *LocalMemoryMetricStore) monitor() {
	begin := time.Now()
	defer func() {
		klog.Infof("[LocalMemoryMetricStore] monitor costs %s", time.Since(begin).String())
	}()

	names := l.cache.ListAllMetricNames()
	klog.Infof("currently with %v metric: %v", len(names), names)
}

// parseMetricSeries parses the given data.MetricSeries into internalMetric
func (l *LocalMemoryMetricStore) parseMetricSeries(series *data.MetricSeries) (types.Metric, bool) {
	// skip already out-of-dated metric contents
	expiredTime := time.Now().Add(-1 * l.genericConf.OutOfDataPeriod).UnixMilli()

	res := types.NewSeriesMetric()

	metricMeta := types.MetricMetaImp{Name: series.Name}
	objectMeta := types.ObjectMetaImp{}
	basicLabel := make(map[string]string)
	for key, value := range series.Labels {
		switch data.CustomMetricLabelKey(key) {
		case data.CustomMetricLabelKeyNamespace:
			metricMeta.Namespaced = true
			objectMeta.ObjectNamespace = value
		case data.CustomMetricLabelKeyObject:
			metricMeta.ObjectKind = value
		case data.CustomMetricLabelKeyObjectName:
			objectMeta.ObjectName = value
		default:
			if strings.HasPrefix(key, fmt.Sprintf("%v", data.CustomMetricLabelSelectorPrefixKey)) {
				basicLabel[strings.TrimPrefix(key, fmt.Sprintf("%v", data.CustomMetricLabelSelectorPrefixKey))] = value
			}
		}
	}
	res.MetricMetaImp = metricMeta
	res.ObjectMetaImp = objectMeta
	res.BasicMetric = types.BasicMetric{Labels: basicLabel}

	if res.GetObjectKind() != "" {
		if res.GetObjectName() == "" {
			return nil, false
		}

		_, err := l.getObject(res.GetObjectKind(), res.GetObjectNamespace(), res.GetObjectName())
		if err != nil {
			klog.Errorf("invalid objects %v %v/%v: %v", res.GetObjectKind(), res.GetObjectNamespace(), res.GetObjectName(), err)
			return nil, false
		}
	}

	for _, m := range series.Series {
		if m.Timestamp < expiredTime {
			continue
		}
		res.AddMetric(&types.SeriesItem{Value: m.Data, Timestamp: m.Timestamp})
	}
	return res, true
}

// checkInternalMetricMatchedWithObject checks if the internal matches with kubernetes object
// the kubernetes object should be obtained by namespace/name
// if not, return an error to represent the unmatched reasons
func (l *LocalMemoryMetricStore) checkInternalMetricMatchedWithObject(internal types.Metric,
	gr *schema.GroupResource, namespace, name string) (bool, error) {
	if gr != nil && gr.String() != internal.GetObjectKind() {
		klog.V(5).Infof("gvr %+v not match with objects %v", gr, internal.GetObjectKind())
		return false, nil
	}

	if internal.GetObjectName() != name {
		klog.V(5).Infof("%v namespace %v not match objectName %v", internal.GetName(), namespace, name)
		return false, nil
	}

	_, err := l.getObject(internal.GetObjectKind(), namespace, name)
	if err != nil {
		return false, err
	}

	return true, nil
}

// checkInternalMetricMatchedWithObject checks if the internal matches with kubernetes object
// the kubernetes object should be obtained by label selector
// if not, return an error to represent the unmatched reasons
func (l *LocalMemoryMetricStore) checkInternalMetricMatchedWithObjectList(internal types.Metric,
	gr *schema.GroupResource, namespace string, selector labels.Selector) (bool, error) {
	if gr != nil && gr.String() != internal.GetObjectKind() {
		klog.V(5).Infof("gvr %+v not match with objects %v", gr, internal.GetObjectKind())
		return false, nil
	}

	obj, err := l.getObject(internal.GetObjectKind(), namespace, internal.GetObjectName())
	if err != nil {
		klog.V(5).Infof("get object %v/%v kind %s failed, %v", namespace, internal.GetName(), internal.GetObjectKind(), err)
		return false, nil
	}

	workload, ok := obj.(*v1.PartialObjectMetadata)
	if !ok {
		return false, fmt.Errorf("%#v failed to transform into PartialObjectMetadata", obj)
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

	lister, ok := l.objectLister[gvr]
	if !ok {
		return nil, fmt.Errorf("unsupported obejct: %v", gvr)
	}

	if namespace != "" {
		return lister.ByNamespace(namespace).Get(name)
	}
	return lister.Get(name)
}

func (l *LocalMemoryMetricStore) getObjectList(gvr, namespace string, selector labels.Selector) ([]runtime.Object, error) {
	lister, ok := l.objectLister[gvr]
	if !ok {
		return nil, fmt.Errorf("unsupported obejct: %v", gvr)
	}

	if namespace != "" {
		return lister.ByNamespace(namespace).List(selector)
	}
	return lister.List(selector)
}
