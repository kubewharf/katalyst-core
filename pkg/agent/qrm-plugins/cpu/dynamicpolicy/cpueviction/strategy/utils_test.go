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

package strategy

import (
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	constsapi "github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestParseNumaIDFormPod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pod     *v1.Pod
		want    string
		wantErr bool
	}{
		{
			name:    "pod is nil",
			pod:     nil,
			want:    "",
			wantErr: true,
		},
		{
			name:    "pod anno is nil",
			pod:     &v1.Pod{},
			want:    "",
			wantErr: true,
		},
		{
			name: "pod without numa binding annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			want:    "",
			wantErr: true,
		},
		{
			name: "pod with invalid numa binding annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constsapi.PodAnnotationNUMABindResultKey: "xxx",
					},
				},
			},
			want:    "xxx",
			wantErr: false,
		},
		{
			name: "normal pod with numa binding annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test1",
					Namespace: "default",
					Annotations: map[string]string{
						constsapi.PodAnnotationNUMABindResultKey: "0",
					},
				},
			},
			want:    "0",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseNumaIDFormPod(tt.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseNumaIDFormPod() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseNumaIDFormPod() got = %v, want %v", got, tt.want)
			}
		})
	}
}
