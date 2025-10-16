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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	npd "github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	rputil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

const testDir = "/tmp/metaserver_test"

func generateTestConfiguration(t *testing.T, dir string) *pkgconfig.Configuration {
	conf := pkgconfig.NewConfiguration()
	require.NotNil(t, conf)
	conf.CheckpointManagerDir = dir
	conf.ConfigCheckpointGraceTime = 2 * time.Hour
	return conf
}

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

	dir := testDir + "/TestNewResourcePackageManager"
	_ = os.RemoveAll(dir)
	require.NoError(t, os.MkdirAll(dir, os.FileMode(0o755)))

	conf := generateTestConfiguration(t, dir)
	mgr, err := NewResourcePackageManager(npd.NewDummyNPDFetcher(), conf, &metrics.DummyMetrics{})
	require.NoError(t, err)
	require.NotNil(t, mgr)
	_ = os.RemoveAll(dir)
}

func TestConvertNPDResourcePackages(t *testing.T) {
	t.Parallel()

	// Build a fake NPD with one metric package
	npdObj := generateTestNPD("node-a")

	mgr := &resourcePackageManager{}
	pkgs, err := mgr.ConvertNPDResourcePackages(npdObj)
	require.NoError(t, err)
	require.NotNil(t, pkgs)
	require.Len(t, pkgs, 1)
}

func TestNodeResourcePackages(t *testing.T) {
	t.Parallel()

	mgr := &resourcePackageManager{
		fetcher: npd.NewDummyNPDFetcher(),
		npd:     generateTestNPD("node-a"),
	}
	pkgs, err := mgr.NodeResourcePackages(context.Background())
	require.NoError(t, err)
	require.NotNil(t, pkgs)
	require.Len(t, pkgs, 1)
}

func TestUpdateNodeProfileDescriptor(t *testing.T) {
	t.Parallel()
	// set up
	dir := testDir + "/TestUpdateNodeProfileDescriptor/load"
	_ = os.RemoveAll(dir)
	require.NoError(t, os.MkdirAll(dir, os.FileMode(0o755)))
	conf := generateTestConfiguration(t, dir)
	pm, err := NewResourcePackageManager(npd.NewDummyNPDFetcher(), conf, &metrics.DummyMetrics{})
	require.NoError(t, err)
	mgrImpl, ok := pm.(*resourcePackageManager)
	require.True(t, ok)

	tests := []struct {
		name    string
		setup   func(t *testing.T) *resourcePackageManager
		wantErr bool
		verify  func(t *testing.T, mgr *resourcePackageManager)
	}{
		{
			name: "load from local disk",
			setup: func(t *testing.T) *resourcePackageManager {
				cp := NewCheckpoint(NodeProfileData{})
				npdObj := generateTestNPD("node-from-disk")
				cp.SetProfile(npdObj, metav1.Now())
				require.NoError(t, mgrImpl.checkpointManager.CreateCheckpoint(resourcePackageManagerCheckpoint, cp))
				return &resourcePackageManager{
					fetcher:             &npd.DummyNPDFetcher{NPD: nil},
					checkpointManager:   mgrImpl.checkpointManager,
					checkpointGraceTime: conf.ConfigCheckpointGraceTime,
					emitter:             &metrics.DummyMetrics{},
				}
			},
			wantErr: false,
			verify: func(t *testing.T, mgr *resourcePackageManager) {
				require.NotNil(t, mgr.npd)
				require.Equal(t, "node-from-disk", mgr.npd.Name)
				require.Equal(t, 2, len(mgr.npd.Status.NodeMetrics[0].Metrics))
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr := tc.setup(t)
			err := mgr.updateNodeProfileDescriptor(context.Background())
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				if tc.verify != nil {
					tc.verify(t, mgr)
				}
			}
		})
	}
	// clean up
	_ = os.RemoveAll(dir)
}
