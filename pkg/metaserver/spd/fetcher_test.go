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
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T, nodeName string, checkpoint string) *pkgconfig.Configuration {
	t.Parallel()

	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	testConfiguration.NodeName = nodeName
	testConfiguration.CheckpointManagerDir = checkpoint
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
		t.Run(tt.name, func(t *testing.T) {
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

			ctx := context.TODO()

			_, _ = s.GetSPD(ctx, tt.args.pod)
			go s.Run(ctx)
			time.Sleep(1 * time.Second)

			got, err := s.GetSPD(ctx, tt.args.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSPD() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			require.Equal(t, tt.want, got)

			// second GetSPD from local cache
			got, err = s.GetSPD(ctx, tt.args.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSPD() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			require.Equal(t, tt.want, got)
		})
	}
}
