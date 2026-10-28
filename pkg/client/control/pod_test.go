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

package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestRealPodUpdaterUpdatePodResize(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	called := false
	client.PrependReactor("update", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		update := action.(k8stesting.UpdateAction)
		assert.Equal(t, "resize", update.GetSubresource())

		pod := update.GetObject().(*core.Pod)
		assert.Equal(t, "pod", pod.Name)
		called = true
		return true, pod, nil
	})

	updater := NewRealPodUpdater(client)
	_, err := updater.UpdatePodResize(context.Background(), &core.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
	}, metav1.UpdateOptions{})

	assert.NoError(t, err)
	assert.True(t, called)
}
