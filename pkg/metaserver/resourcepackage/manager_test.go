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

package resourcepackage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	npd "github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	rputil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

func generateTestNPD(nodeName string) *nodev1alpha1.NodeProfileDescriptor {
	min := nodev1alpha1.AggregatorMin
	return &nodev1alpha1.NodeProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Status: nodev1alpha1.NodeProfileDescriptorStatus{
			NodeMetrics: []nodev1alpha1.ScopedNodeMetrics{
				{
					Scope: rputil.MetricScope,
					Metrics: []nodev1alpha1.MetricValue{
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"package-name": "x2",
								"numa-id":      "0",
							},
							Value:      resource.MustParse("32"),
							Aggregator: &min,
						},
						{
							MetricName: "memory",
							MetricLabels: map[string]string{
								"package-name": "x2",
								"numa-id":      "0",
							},
							Value:      resource.MustParse("64Gi"),
							Aggregator: &min,
						},
					},
				},
			},
		},
	}
}

func TestNewResourcePackageManager(t *testing.T) {
	t.Parallel()

	mgr := NewResourcePackageManager(npd.NewDummyNPDFetcher())
	require.NotNil(t, mgr)
}

func TestConvertNPDResourcePackages(t *testing.T) {
	t.Parallel()

	mgr := &resourcePackageManager{}
	pkgs, err := mgr.ConvertNPDResourcePackages(generateTestNPD("node-a"))
	require.NoError(t, err)
	require.NotNil(t, pkgs)
	require.Len(t, pkgs, 1)
}

func TestNodeResourcePackages(t *testing.T) {
	t.Parallel()

	mgr := &resourcePackageManager{
		fetcher: &npd.DummyNPDFetcher{
			NPD: generateTestNPD("node-a"),
		},
	}
	pkgs, err := mgr.NodeResourcePackages(context.Background())
	require.NoError(t, err)
	require.NotNil(t, pkgs)
	require.Len(t, pkgs, 1)
}
