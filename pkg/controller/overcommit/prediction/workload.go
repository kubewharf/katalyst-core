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

package prediction

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/predictor/nsigma"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/provider/prom"
)

func (p *Prediction) initPredictor() error {
	switch p.conf.Prediction.Predictor {
	case nsigma.NSigmaPredictor:
		p.predictor = nsigma.NewPredictor(p.conf.Prediction.Factor, p.conf.Prediction.Buckets)
	case "":
		klog.Infof("predictor not set, skip")
	default:
		return fmt.Errorf("predictor %v not support yet", p.conf.Prediction.Predictor)
	}

	return nil
}

func (p *Prediction) initProvider() error {
	if p.conf.Prediction.Address == "" {
		klog.Warning("provider address not set")
		return nil
	}
	// init provider
	promProvider, err := prom.NewProvider(p.conf.Prediction.PromConfig)
	if err != nil {
		err = fmt.Errorf("new prom provider fail: %v", err)
		return err
	}
	p.provider = promProvider
	return nil
}

// predict workloads cpu and memory usage by history time series
func (p *Prediction) reconcileWorkloads() {
	for gvr, lister := range p.workloadLister {
		objs, err := lister.List(labels.Everything())
		if err != nil {
			klog.Errorf("workload %v list fail: %v", gvr.String(), err)
			continue
		}
		klog.V(6).Infof("reconcile %v workload, length: %v", gvr.String(), len(objs))

		for _, obj := range objs {
			err := p.predictWorkload(obj)
			if err != nil {
				klog.Error(err)
				continue
			}
		}
	}
	p.firstReconcileWorkload = true
}

func (p *Prediction) predictWorkload(obj runtime.Object) error {
	workload := obj.(*unstructured.Unstructured)

	predictTimeSeriesRes := make(map[string]*common.TimeSeries, 0)
	for resource := range resourceToPortraitMetrics {
		// generate query
		query, err := p.generateQuery(resource, workload)
		if err != nil {
			klog.Errorf("%v %v generateQuery fail: %v", workload.GetKind(), workload.GetName(), err)
			return err
		}

		// request metrics
		timeSeries, err := p.requestHistoryTimeSeries(query)
		if err != nil {
			klog.Errorf("%v %v requestHistoryTimeSeries fail: %v", workload.GetKind(), workload.GetName(), err)
			return err
		}

		// validate timeSeries
		ok, err := p.validateTimeSeries(timeSeries)
		if !ok {
			klog.V(6).Infof("workload %v validateTimeSeries: %v", workload.GetName(), err)
			return err
		}

		// predict
		predictTimeSeries, err := p.predictor.PredictTimeSeries(p.ctx, &common.PredictArgs{WorkloadName: workload.GetName(), ResourceName: resource}, timeSeries[0])
		if err != nil {
			klog.Errorf("%v %v PredictTimeSeries fail: %v", workload.GetKind(), workload.GetName(), err)
			return err
		}

		klog.V(6).Infof("%v %v predict timeSeries: %v", workload.GetKind(), workload.GetName(), predictTimeSeries.Samples)
		predictTimeSeriesRes[resource] = predictTimeSeries
	}

	err := p.updateWorkloadUsageCache(workload.GetNamespace(), workload.GetKind(), workload.GetName(), predictTimeSeriesRes)
	if err != nil {
		klog.Errorf("%v %v update resource portrait result fail: %v", workload.GetKind(), workload.GetName(), err)
	}

	return nil
}

func (p *Prediction) generateQuery(resourceName string, workload *unstructured.Unstructured) (string, error) {
	matchLabels := []common.Metadata{
		{Key: namespaceMatchKey, Value: workload.GetNamespace()},
		{Key: podMatchKey, Value: podNameByWorkload(workload.GetName(), workload.GetKind())},
		{Key: containerMatchKey, Value: ""},
	}

	return p.provider.BuildQuery(resourceName, matchLabels)
}

func (p *Prediction) requestHistoryTimeSeries(query string) ([]*common.TimeSeries, error) {
	now := time.Now()
	endTime := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, time.Local)
	startTime := endTime.Add(-p.conf.Prediction.MaxTimeSeriesDuration)

	return p.provider.QueryTimeSeries(p.ctx, query, startTime, endTime, defaultStep)
}

func (p *Prediction) validateTimeSeries(timeSeries []*common.TimeSeries) (bool, error) {
	if len(timeSeries) <= 0 {
		return false, fmt.Errorf("timeSeries without data")
	}

	if len(timeSeries) > 1 {
		return false, fmt.Errorf("more than 1 timeSeries")
	}

	startTime := timeSeries[0].Samples[0].Timestamp
	endTime := timeSeries[0].Samples[len(timeSeries[0].Samples)-1].Timestamp

	if p.conf.Prediction.MinTimeSeriesDuration > time.Duration(endTime-startTime)*time.Second {
		return false, fmt.Errorf("not enough data, startTime: %v, endTime: %v, minDuration: %v", startTime, endTime, p.conf.Prediction.MinTimeSeriesDuration)
	}

	return true, nil
}

func (p *Prediction) updateWorkloadUsageCache(namespace, workloadType, workloadName string, metricTimeSeries map[string]*common.TimeSeries) error {
	if len(metricTimeSeries) <= 0 {
		return fmt.Errorf("update workload %v usage cache, get null timeSeries", workloadName)
	}

	cacheKey := workloadUsageCacheName(namespace, workloadType, workloadName)

	p.Lock()
	defer p.Unlock()

	if p.workloadUsageCache == nil {
		p.workloadUsageCache = make(map[string]map[string]*common.TimeSeries)
	}
	p.workloadUsageCache[cacheKey] = metricTimeSeries

	return nil
}
