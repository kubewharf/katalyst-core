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

// Package store is the package that stores the real-time metric,
// katalyst may support different kinds of store implementations for
// different scenarios, such monolith im-memory store or distributed
// im-memory stores.
package store

import (
	"context"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
)

// MetricStore is a standard metric storage interface
type MetricStore interface {
	Name() string

	// Start and Stop are both blocked functions
	Start() error
	Stop() error

	// InsertMetric receives metric data items, and put them into storages;
	// this put action may not be atomic, and it's possible that some items
	// are succeeded, while others are not; if any item is failed to be inserted,
	// an error will be returned.
	InsertMetric(s []*data.MetricSeries) error

	// GetMetric returns the metric data items according to the given
	// metrics references; if sharding is needed, it should be implemented
	// as a specific MetricStore inner logic.
	GetMetric(ctx context.Context, namespace, metricName, objName string, gr *schema.GroupResource,
		objSelector, metricSelector labels.Selector, latest bool) ([]types.Metric, error)
	// ListMetricMeta returns all metrics type bounded with a kubernetes objects if withObject is true
	ListMetricMeta(ctx context.Context, withObject bool) ([]types.MetricMeta, error)
}
