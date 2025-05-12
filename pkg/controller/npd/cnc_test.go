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

package npd

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	listerv1alpha1 "github.com/kubewharf/katalyst-api/pkg/client/listers/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
)

func Test_applyNPDTargetConfigToCNC(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		npd       *v1alpha1.NodeProfileDescriptor
		cnc       *configv1alpha1.CustomNodeConfig
		expectCNC *configv1alpha1.CustomNodeConfig
	}{
		{
			name: "update cnc",
			npd:  fakeNPD(),
			cnc: &configv1alpha1.CustomNodeConfig{
				Status: configv1alpha1.CustomNodeConfigStatus{
					KatalystCustomConfigList: []configv1alpha1.TargetConfig{
						{
							ConfigType: npdGVR,
							ConfigName: "node-1",
							Hash:       "old-hash",
						}, {
							ConfigType: metav1.GroupVersionResource{},
							ConfigName: "other",
							Hash:       "111",
						},
					},
				},
			},
			expectCNC: &configv1alpha1.CustomNodeConfig{
				Status: configv1alpha1.CustomNodeConfigStatus{
					KatalystCustomConfigList: []configv1alpha1.TargetConfig{
						{
							ConfigType: npdGVR,
							ConfigName: "node-1",
							Hash:       "e3590b46f999",
						}, {
							ConfigType: metav1.GroupVersionResource{},
							ConfigName: "other",
							Hash:       "111",
						},
					},
				},
			},
		}, {
			name: "add cnc target",
			npd:  fakeNPD(),
			cnc: &configv1alpha1.CustomNodeConfig{
				Status: configv1alpha1.CustomNodeConfigStatus{
					KatalystCustomConfigList: []configv1alpha1.TargetConfig{
						{
							ConfigType: metav1.GroupVersionResource{},
							ConfigName: "other",
							Hash:       "111",
						},
					},
				},
			},
			expectCNC: &configv1alpha1.CustomNodeConfig{
				Status: configv1alpha1.CustomNodeConfigStatus{
					KatalystCustomConfigList: []configv1alpha1.TargetConfig{
						{
							ConfigType: metav1.GroupVersionResource{},
							ConfigName: "other",
							Hash:       "111",
						}, {
							ConfigType: npdGVR,
							ConfigName: "node-1",
							Hash:       "e3590b46f999",
						},
					},
				},
			},
		}, {
			name: "not need to update",
			npd:  fakeNPD(),
			cnc: &configv1alpha1.CustomNodeConfig{
				Status: configv1alpha1.CustomNodeConfigStatus{
					KatalystCustomConfigList: []configv1alpha1.TargetConfig{
						{
							ConfigType: metav1.GroupVersionResource{},
							ConfigName: "other",
							Hash:       "111",
						}, {
							ConfigType: npdGVR,
							ConfigName: "node-1",
							Hash:       "e3590b46f999",
						},
					},
				},
			},
			expectCNC: &configv1alpha1.CustomNodeConfig{
				Status: configv1alpha1.CustomNodeConfigStatus{
					KatalystCustomConfigList: []configv1alpha1.TargetConfig{
						{
							ConfigType: metav1.GroupVersionResource{},
							ConfigName: "other",
							Hash:       "111",
						}, {
							ConfigType: npdGVR,
							ConfigName: "node-1",
							Hash:       "e3590b46f999",
						},
					},
				},
			},
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nc := &NPDController{
				ctx:        context.Background(),
				cncLister:  newFakeCNCLister(tt.cnc),
				cncControl: control.DummyCNCControl{},
			}

			err := nc.applyNPDTargetConfigToCNC(tt.npd)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectCNC, tt.cnc)
		})
	}
}

type fakeCNCLister struct {
	cnc *configv1alpha1.CustomNodeConfig
}

func newFakeCNCLister(cnc *configv1alpha1.CustomNodeConfig) listerv1alpha1.CustomNodeConfigLister {
	return &fakeCNCLister{
		cnc: cnc,
	}
}

func (f *fakeCNCLister) List(selector labels.Selector) (ret []*configv1alpha1.CustomNodeConfig, err error) {
	return []*configv1alpha1.CustomNodeConfig{f.cnc}, nil
}

func (f *fakeCNCLister) Get(name string) (*configv1alpha1.CustomNodeConfig, error) {
	return f.cnc, nil
}

func TestCalculateNPDHash(t *testing.T) {
	t.Parallel()

	type args struct {
		npd *v1alpha1.NodeProfileDescriptor
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "nil spd",
			args: args{
				npd: nil,
			},
			want:    "",
			wantErr: assert.Error,
		},
		{
			name: "test calculate npd hash",
			args: args{
				npd: fakeNPD(),
			},
			want:    "e3590b46f999",
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CalculateNPDHash(tt.args.npd)
			if !tt.wantErr(t, err, fmt.Sprintf("CalculateNPDHash(%v)", tt.args.npd)) {
				return
			}
			assert.Equalf(t, tt.want, got, "CalculateNPDHash(%v)", tt.args.npd)
		})
	}
}

func fakeNPD() *v1alpha1.NodeProfileDescriptor {
	return &v1alpha1.NodeProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
		},
		Status: v1alpha1.NodeProfileDescriptorStatus{
			NodeMetrics: []v1alpha1.ScopedNodeMetrics{
				{
					Scope: "resource-level",
					Metrics: []v1alpha1.MetricValue{
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"level": "L1",
							},
							Value: resource.MustParse("4"),
						},
						{
							MetricName: "memory",
							MetricLabels: map[string]string{
								"level": "L1",
							},
							Value: resource.MustParse("4Gi"),
						},
					},
				},
			},
		},
	}
}
