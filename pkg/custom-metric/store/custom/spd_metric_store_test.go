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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	katalystmetric "github.com/kubewharf/katalyst-api/pkg/metric"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	resourceportrait "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin/plugins/resource-portrait"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
)

func TestIHPAMetricStore(t *testing.T) {
	t.Parallel()

	genericContext, err := katalystbase.GenerateFakeGenericContext()
	assert.NoError(t, err)

	s, err := NewSPDMetricStore(context.TODO(), genericContext, nil, nil)
	assert.Nil(t, err)

	t.Run("Name", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, SPDCustomMetricStore, s.Name())
	})

	t.Run("InsertMetric", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, s.InsertMetric(nil))
	})

	t.Run("Start", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, s.Start())
	})

	t.Run("Stop", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, s.Stop())
	})

	t.Run("ListMetricMeta", func(t *testing.T) {
		t.Parallel()
		metas, err := s.ListMetricMeta(context.TODO(), false)
		assert.NoError(t, err)
		assert.Equal(t, []types.MetricMeta{types.MetricMetaImp{
			Name: "spd_agg_metrics",
		}}, metas)
	})

	t.Run("GetMetric", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		spd := &v1alpha1.ServiceProfileDescriptor{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "default"},
			Status: v1alpha1.ServiceProfileDescriptorStatus{
				AggMetrics: []v1alpha1.AggPodMetrics{
					{
						Scope: resourceportrait.ResourcePortraitPluginName,
						Items: []v1beta1.PodMetrics{
							{
								Containers: []v1beta1.ContainerMetrics{
									{
										Name: "ihpa-predict",
										Usage: v1.ResourceList{
											"default": resource.MustParse("100m"),
										},
									},
								},
								Timestamp: metav1.Time{Time: now.Add(-time.Minute * 10)},
							},
							{
								Containers: []v1beta1.ContainerMetrics{
									{
										Name: "ihpa-predict",
										Usage: v1.ResourceList{
											"default": resource.MustParse("200m"),
										},
									},
									{
										Name: "ihpa-ihpa",
										Usage: v1.ResourceList{
											"default": resource.MustParse("200m"),
										},
									},
								},
								Timestamp: metav1.Time{Time: now.Add(-time.Minute)},
							},
						},
					},
					{
						Scope: resourceportrait.ResourcePortraitPluginName + "test",
						Items: []v1beta1.PodMetrics{
							{
								Containers: []v1beta1.ContainerMetrics{
									{
										Name: "ihpa-predict",
										Usage: v1.ResourceList{
											"default": resource.MustParse("300m"),
										},
									},
								},
								Timestamp: metav1.Time{Time: now.Add(time.Minute * 10)},
							},
						},
					},
				},
			},
		}

		inf := genericContext.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Informer()
		err := inf.GetStore().Add(spd)
		assert.NoError(t, err)

		labelSelector := labels.NewSelector()
		r1, err := labels.NewRequirement(katalystmetric.MetricSelectorKeySPDName, selection.Equals, []string{"default"})
		assert.NoError(t, err)
		r2, err := labels.NewRequirement(katalystmetric.MetricSelectorKeySPDResourceName, selection.Equals, []string{"default"})
		assert.NoError(t, err)
		r3, err := labels.NewRequirement(katalystmetric.MetricSelectorKeySPDContainerName, selection.Equals, []string{"ihpa-predict"})
		assert.NoError(t, err)
		r4, err := labels.NewRequirement(katalystmetric.MetricSelectorKeySPDScopeName, selection.Equals, []string{"ResourcePortraitIndicatorPlugin"})
		assert.NoError(t, err)
		labelSelector = labelSelector.Add(*r1, *r2, *r3, *r4)

		expected := []types.Metric{&types.SeriesMetric{MetricMetaImp: types.MetricMetaImp{Name: "spd_agg_metrics"}, Values: []*types.SeriesItem{types.NewInternalItem(0.2, now.Add(-time.Minute).UnixMilli())}}}
		metrics, err := s.GetMetric(context.TODO(), "default", "spd_agg_metrics", "", nil, nil, labelSelector, false)
		assert.NoError(t, err)
		assert.Equal(t, expected, metrics)
		assert.Equal(t, SPDCustomMetricStore, s.Name())

		labelSelector = labels.NewSelector()
		_, err = s.GetMetric(context.TODO(), "default", "ihpa_prediction_metric", "", nil, nil, labelSelector, false)
		assert.Error(t, err)
	})
}
