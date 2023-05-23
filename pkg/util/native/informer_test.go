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

	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodTransformer(t *testing.T) {
	as := require.New(t)

	WithPodTransformer(func(src, dest *core.Pod) {
		dest.Spec.NodeName = src.Spec.NodeName
	})

	transform, ok := GetPodTransformer()
	as.Equal(true, ok)

	p := &core.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pod-1",
			Annotations: map[string]string{
				"t-anno-key": "ann",
			},
			Labels: map[string]string{
				"t-label-key": "label",
			},
		},
		Spec: core.PodSpec{
			NodeName: "t-node",
			Containers: []core.Container{
				{},
			},
		},
		Status: core.PodStatus{
			Phase: core.PodPending,
		},
	}
	p1, err := transform(p)
	as.NoError(err)
	as.Equal(core.PodStatus{}, p1.(*core.Pod).Status)
	as.Equal(core.PodSpec{NodeName: "t-node"}, p1.(*core.Pod).Spec)
	as.Equal(p.ObjectMeta, p1.(*core.Pod).ObjectMeta)
}
