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

package native

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodUniqKeyCmpFunc(t *testing.T) {
	type args struct {
		i1 *v1.Pod
		i2 *v1.Pod
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "greater",
			args: args{
				i1: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-a",
						Namespace: "namespace-1",
					},
				},
				i2: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-b",
						Namespace: "namespace-1",
					},
				},
			},
			want: 1,
		},
		{
			name: "equal",
			args: args{
				i1: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-a",
						Namespace: "namespace-1",
					},
				},
				i2: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-a",
						Namespace: "namespace-1",
					},
				},
			},
			want: 0,
		},
		{
			name: "smaller",
			args: args{
				i1: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-b",
						Namespace: "namespace-1",
					},
				},
				i2: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-a",
						Namespace: "namespace-1",
					},
				},
			},
			want: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, PodUniqKeyCmpFunc(tt.args.i1, tt.args.i2), "PodUniqKeyCmpFunc(%v, %v)", tt.args.i1, tt.args.i2)
		})
	}
}
