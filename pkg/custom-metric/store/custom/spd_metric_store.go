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

package custom

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metrics "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	katalystmetric "github.com/kubewharf/katalyst-api/pkg/metric"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	metricconf "github.com/kubewharf/katalyst-core/pkg/config/metric"
	resourceportrait "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin/plugins/resource-portrait"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
)

const (
	SPDCustomMetricStore = "spd-metric-store"
)

type SPDMetricStore struct {
	metricName string
	spdLister  v1alpha1.ServiceProfileDescriptorLister
}

func NewSPDMetricStore(_ context.Context, baseCtx *katalystbase.GenericContext,
	_ *metricconf.GenericMetricConfiguration, _ *metricconf.StoreConfiguration,
) (store.MetricStore, error) {
	return &SPDMetricStore{
		metricName: katalystmetric.MetricNameSPDAggMetrics,
		spdLister:  baseCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Lister(),
	}, nil
}

func (s *SPDMetricStore) Name() string {
	return SPDCustomMetricStore
}

func (s *SPDMetricStore) Start() error {
	return nil
}

func (s *SPDMetricStore) Stop() error {
	return nil
}

func (s *SPDMetricStore) InsertMetric(_ []*data.MetricSeries) error {
	return nil
}

func (s *SPDMetricStore) GetMetric(_ context.Context, namespace, metricName, _ string, _ *schema.GroupResource,
	_, metricSelector labels.Selector, _ bool,
) ([]types.Metric, error) {
	if metricName != s.metricName {
		return nil, fmt.Errorf("metric name %s does not match store's metric name %s", metricName, s.metricName)
	}

	if metricSelector == nil {
		return nil, fmt.Errorf("metric selector cannot be nil")
	}

	labelMap, _ := labels.ConvertSelectorToLabelsMap(metricSelector.String())
	metricName = labelMap[katalystmetric.MetricSelectorKeySPDResourceName]
	if metricName == "" {
		return nil, fmt.Errorf("ihpa get metric err: empty metric name")
	}

	spd, err := s.spdLister.ServiceProfileDescriptors(namespace).Get(labelMap[katalystmetric.MetricSelectorKeySPDName])
	if err != nil {
		return nil, err
	}

	var currentMetric *metrics.PodMetrics
	for _, aggMetrics := range spd.Status.AggMetrics {
		if aggMetrics.Scope != resourceportrait.ResourcePortraitPluginName {
			continue
		}

		now := time.Now()
		for _, item := range aggMetrics.Items {
			if now.After(item.Timestamp.Time) {
				currentMetric = item.DeepCopy()
			} else {
				break
			}
		}
	}

	if currentMetric == nil {
		return nil, fmt.Errorf("no metric found for %s", metricName)
	}

	for _, container := range currentMetric.Containers {
		if container.Name != labelMap[katalystmetric.MetricSelectorKeySPDContainerName] {
			continue
		}
		if value, ok := container.Usage[v1.ResourceName(metricName)]; ok {
			return []types.Metric{&types.SeriesMetric{MetricMetaImp: types.MetricMetaImp{Name: s.metricName}, Values: []*types.SeriesItem{types.NewInternalItem(float64(value.MilliValue()), currentMetric.Timestamp.UnixMilli())}}}, nil
		}
	}
	return nil, fmt.Errorf("no metric found for %s", metricName)
}

// ListMetricMeta usually only returns a limited number of predefined metric names. These metric names can be considered to be a broad category of metrics, and the specific metrics used can be clarified through more detailed labels,
// for External type indicators.
func (s *SPDMetricStore) ListMetricMeta(_ context.Context, _ bool) ([]types.MetricMeta, error) {
	return []types.MetricMeta{types.MetricMetaImp{
		Name: s.metricName,
	}}, nil
}
