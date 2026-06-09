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

package spd

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T, nodeName string, checkpoint string) *pkgconfig.Configuration {
	testConfiguration := pkgconfig.NewConfiguration()
	require.NotNil(t, testConfiguration)

	testConfiguration.NodeName = nodeName
	testConfiguration.ServiceProfileCacheTTL = 1 * time.Minute
	testConfiguration.CheckpointManagerDir = checkpoint
	testConfiguration.ServiceProfileEnableNamespaces = []string{"*"}
	testConfiguration.SPDGetFromRemote = true
	return testConfiguration
}

func Test_spdManager_GetSPD(t *testing.T) {
	t.Parallel()

	type fields struct {
		nodeName string
		spd      *workloadapis.ServiceProfileDescriptor
		cnc      *v1alpha1.CustomNodeConfig
	}
	type args struct {
		pod *v1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *workloadapis.ServiceProfileDescriptor
		wantErr bool
	}{
		{
			name: "test-1",
			fields: fields{
				nodeName: "node-1",
				spd: &workloadapis.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "spd-1",
						Namespace: "default",
						Annotations: map[string]string{
							pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "3c7e3ff3f218",
						},
					},
				},
				cnc: &v1alpha1.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						ServiceProfileConfigList: []v1alpha1.TargetConfig{
							{
								ConfigName:      "spd-1",
								ConfigNamespace: "default",
								Hash:            "3c7e3ff3f218",
							},
						},
					},
				},
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.PodAnnotationSPDNameKey: "spd-1",
						},
					},
				},
			},
			want: &workloadapis.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spd-1",
					Namespace: "default",
					Annotations: map[string]string{
						pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "3c7e3ff3f218",
					},
				},
			},
		},
		{
			name: "test-2",
			fields: fields{
				nodeName: "node-1",
				spd: &workloadapis.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "spd-1",
						Namespace: "default",
						Annotations: map[string]string{
							pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "3c7e3ff3f218",
						},
					},
					Spec: workloadapis.ServiceProfileDescriptorSpec{
						BusinessIndicator: []workloadapis.ServiceBusinessIndicatorSpec{
							{
								Name: workloadapis.ServiceBusinessIndicatorNameRPCLatency,
							},
						},
					},
				},
				cnc: &v1alpha1.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						ServiceProfileConfigList: []v1alpha1.TargetConfig{
							{
								ConfigName:      "spd-1",
								ConfigNamespace: "default",
								Hash:            "3c7e3ff3f218",
							},
						},
					},
				},
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.PodAnnotationSPDNameKey: "spd-1",
						},
					},
				},
			},
			want: &workloadapis.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spd-1",
					Namespace: "default",
					Annotations: map[string]string{
						pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "3c7e3ff3f218",
					},
				},
				Spec: workloadapis.ServiceProfileDescriptorSpec{
					BusinessIndicator: []workloadapis.ServiceBusinessIndicatorSpec{
						{
							Name: workloadapis.ServiceBusinessIndicatorNameRPCLatency,
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir, err := ioutil.TempDir("", "checkpoint-Test_spdManager_GetSPD")
			require.NoError(t, err)
			defer os.RemoveAll(dir)

			conf := generateTestConfiguration(t, tt.fields.nodeName, dir)
			genericCtx, err := katalyst_base.GenerateFakeGenericContext(nil, []runtime.Object{
				tt.fields.spd,
				tt.fields.cnc,
			})
			require.NoError(t, err)

			cncFetcher := cnc.NewCachedCNCFetcher(conf.BaseConfiguration, conf.CNCConfiguration, genericCtx.Client.InternalClient.ConfigV1alpha1().CustomNodeConfigs())
			s, err := NewSPDFetcher(genericCtx.Client, metrics.DummyMetrics{}, cncFetcher, conf)
			require.NoError(t, err)
			require.NotNil(t, s)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			_, _ = s.GetSPD(ctx, tt.args.pod.ObjectMeta)
			go s.Run(ctx)
			time.Sleep(1 * time.Second)

			got, err := s.GetSPD(ctx, tt.args.pod.ObjectMeta)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSPD() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			require.Equal(t, tt.want.Spec, got.Spec)
			require.Equal(t, tt.want.Status, got.Status)

			// second GetSPD from local cache
			got, err = s.GetSPD(ctx, tt.args.pod.ObjectMeta)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSPD() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			require.Equal(t, tt.want.Spec, got.Spec)
			require.Equal(t, tt.want.Status, got.Status)
		})
	}
}

func newFetcherForTest(t *testing.T, objects []runtime.Object) (*spdFetcher, func()) {
	dir, err := ioutil.TempDir("", "spd-fallback-test")
	require.NoError(t, err)

	conf := generateTestConfiguration(t, "node-1", dir)
	genericCtx, err := katalyst_base.GenerateFakeGenericContext(nil, objects)
	require.NoError(t, err)

	cncFetcher := cnc.NewCachedCNCFetcher(conf.BaseConfiguration, conf.CNCConfiguration, genericCtx.Client.InternalClient.ConfigV1alpha1().CustomNodeConfigs())
	s, err := NewSPDFetcher(genericCtx.Client, metrics.DummyMetrics{}, cncFetcher, conf)
	require.NoError(t, err)

	cleanup := func() { os.RemoveAll(dir) }
	return s.(*spdFetcher), cleanup
}

// Test_GetSPD_NoFallbackWhenNameMissing verifies that when getPodSPDNameFunc fails
// (e.g. pod has no SPD annotation), GetSPD must NOT fall back to default SPD.
func Test_GetSPD_NoFallbackWhenNameMissing(t *testing.T) {
	t.Parallel()

	defaultSPD := &workloadapis.ServiceProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default-spd",
			Namespace: consts.DefaultClusterSPDNamespace,
			Labels: map[string]string{
				consts.SPDLabelDefaultClusterSPDKey: consts.SPDLabelDefaultClusterSPDValue,
			},
		},
	}
	cncObj := &v1alpha1.CustomNodeConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: v1alpha1.CustomNodeConfigStatus{
			ServiceProfileConfigList: []v1alpha1.TargetConfig{
				{
					ConfigName:      "default-spd",
					ConfigNamespace: consts.DefaultClusterSPDNamespace,
					Hash:            "abc",
				},
			},
		},
	}

	s, cleanup := newFetcherForTest(t, []runtime.Object{defaultSPD, cncObj})
	defer cleanup()

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
			Annotations: map[string]string{
				// even though defaultName is set, fallback must not trigger because
				// the workload-specific SPD name cannot be resolved.
				consts.PodAnnotationDefaultSPDNameKey: "default-spd",
			},
		},
	}

	got, err := s.GetSPD(context.TODO(), pod.ObjectMeta)
	require.Error(t, err)
	require.Nil(t, got)
	require.Equal(t, SPDNameNotFoundError, err)
}

// Test_GetSPD_FallbackOnNotFound verifies fallback behavior when normal SPD name
// is resolved but the SPD itself is not found.
func Test_GetSPD_FallbackOnNotFound(t *testing.T) {
	t.Parallel()

	defaultSPD := &workloadapis.ServiceProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default-spd",
			Namespace: consts.DefaultClusterSPDNamespace,
			Labels: map[string]string{
				consts.SPDLabelDefaultClusterSPDKey: consts.SPDLabelDefaultClusterSPDValue,
			},
			Annotations: map[string]string{
				pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "default-hash",
			},
		},
	}
	cncObj := &v1alpha1.CustomNodeConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: v1alpha1.CustomNodeConfigStatus{
			ServiceProfileConfigList: []v1alpha1.TargetConfig{
				{
					ConfigName:      "default-spd",
					ConfigNamespace: consts.DefaultClusterSPDNamespace,
					Hash:            "default-hash",
				},
			},
		},
	}

	s, cleanup := newFetcherForTest(t, []runtime.Object{defaultSPD, cncObj})
	defer cleanup()

	// pod references a non-existent normal SPD, but provides a valid defaultName
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
			Annotations: map[string]string{
				consts.PodAnnotationSPDNameKey:        "missing-spd",
				consts.PodAnnotationDefaultSPDNameKey: "default-spd",
			},
		},
	}

	got, err := s.GetSPD(context.TODO(), pod.ObjectMeta)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "default-spd", got.Name)
}

// Test_GetSPD_FallbackRequiresLabel verifies that a SPD referenced by defaultName
// without the cluster-default label is rejected.
func Test_GetSPD_FallbackRequiresLabel(t *testing.T) {
	t.Parallel()

	notDefault := &workloadapis.ServiceProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fake-default",
			Namespace: consts.DefaultClusterSPDNamespace,
			// no defaultClusterSPD label
			Annotations: map[string]string{
				pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "h",
			},
		},
	}
	cncObj := &v1alpha1.CustomNodeConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: v1alpha1.CustomNodeConfigStatus{
			ServiceProfileConfigList: []v1alpha1.TargetConfig{
				{
					ConfigName:      "fake-default",
					ConfigNamespace: consts.DefaultClusterSPDNamespace,
					Hash:            "h",
				},
			},
		},
	}

	s, cleanup := newFetcherForTest(t, []runtime.Object{notDefault, cncObj})
	defer cleanup()

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
			Annotations: map[string]string{
				consts.PodAnnotationSPDNameKey:        "missing-spd",
				consts.PodAnnotationDefaultSPDNameKey: "fake-default",
			},
		},
	}

	got, err := s.GetSPD(context.TODO(), pod.ObjectMeta)
	require.Error(t, err)
	require.Nil(t, got)
}

// Test_GetSPD_FallbackNoDefaultAnnotation verifies that when no defaultName
// annotation is set, fallback yields NotFound (not the default spd path's error).
func Test_GetSPD_FallbackNoDefaultAnnotation(t *testing.T) {
	t.Parallel()

	cncObj := &v1alpha1.CustomNodeConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
	}
	s, cleanup := newFetcherForTest(t, []runtime.Object{cncObj})
	defer cleanup()

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
			Annotations: map[string]string{
				consts.PodAnnotationSPDNameKey: "missing-spd",
				// no PodAnnotationDefaultSPDNameKey
			},
		},
	}

	got, err := s.GetSPD(context.TODO(), pod.ObjectMeta)
	require.Error(t, err)
	require.Nil(t, got)
}
