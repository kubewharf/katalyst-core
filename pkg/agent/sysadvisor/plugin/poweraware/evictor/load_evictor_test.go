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

package evictor

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

func Test_loadEvictor_isBE(t *testing.T) {
	t.Parallel()
	qosConfig := generic.NewQoSConfiguration()

	type fields struct {
		qosConfig *generic.QoSConfiguration
	}
	type args struct {
		pod *v1.Pod
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   bool
	}{
		{
			name: "reclaimed core is BE",
			fields: fields{
				qosConfig: qosConfig,
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
					},
				},
			},
			want: true,
		},
		{
			name: "shared core is NOT BE",
			fields: fields{
				qosConfig: qosConfig,
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
					},
				},
			},
			want: false,
		},
		{
			name: "system core is not BE",
			fields: fields{
				qosConfig: qosConfig,
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
						},
					},
				},
			},
			want: false,
		},
		{
			name: "random annotation value is not BE",
			fields: fields{
				qosConfig: qosConfig,
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: "random value",
						},
					},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := loadEvictor{
				qosConfig: qosConfig,
			}
			if got := l.isBE(tt.args.pod); got != tt.want {
				t.Errorf("isBE() = %v, want %v", got, tt.want)
			}
		})
	}
}

type mockPodFetcher struct {
	pod.PodFetcher
}

func (m mockPodFetcher) GetPodList(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
	}, nil
}

func Test_loadEvictor_Evict(t *testing.T) {
	t.Parallel()

	podEvictor := &noopPodEvictor{}
	l := loadEvictor{
		qosConfig:  generic.NewQoSConfiguration(),
		podFetcher: &mockPodFetcher{},
		podEvictor: podEvictor,
	}

	l.Evict(context.TODO(), 100)

	if podEvictor.called != 1 {
		t.Errorf("expected to call 1 times, got %d", podEvictor.called)
	}
}
