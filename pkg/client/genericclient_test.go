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

// Package client is the package that generate K8S kubeConfig and clientSet; and
// any new CRD and its corresponding clientSet should be added here.
// besides, this package is the only package that update/patch actions should happen.
package client // import "github.com/kubewharf/katalyst-core/pkg/client"

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	cmfake "k8s.io/metrics/pkg/client/custom_metrics/fake"
	emfake "k8s.io/metrics/pkg/client/external_metrics/fake"
)

func TestMetricsClient(t *testing.T) {
	t.Parallel()

	client := GenericClientSet{
		CustomClient:   &cmfake.FakeCustomMetricsClient{},
		ExternalClient: &emfake.FakeExternalMetricsClient{},
	}

	objectLabel := labels.Everything()
	metricsLabel := labels.Everything()

	var err error

	_, err = client.CustomClient.RootScopedMetrics().GetForObject(schema.GroupKind{},
		"none-exist-pod", "none-exist-metric", metricsLabel)
	assert.ErrorContains(t, err, "returned 0 results")

	_, err = client.CustomClient.RootScopedMetrics().GetForObjects(schema.GroupKind{},
		objectLabel, "none-exist-metric", metricsLabel)
	assert.Nil(t, err)

	_, err = client.CustomClient.NamespacedMetrics("none-exist-ns").GetForObject(schema.GroupKind{},
		"none-exist-pod", "none-exist-metric", metricsLabel)
	assert.ErrorContains(t, err, "returned 0 results")

	_, err = client.CustomClient.NamespacedMetrics("none-exist-ns").GetForObjects(schema.GroupKind{},
		objectLabel, "none-exist-metric", metricsLabel)
	assert.Nil(t, err)

	_, err = client.ExternalClient.NamespacedMetrics("none-exist-ns").List("none-exist-external-metric", metricsLabel)
	assert.Nil(t, err)
}
