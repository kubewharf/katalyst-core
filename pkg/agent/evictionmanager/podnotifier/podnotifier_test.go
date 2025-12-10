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

package podnotifier

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/rule"
)

// mock implementations for interfaces

type mockNotifier struct {
	NotifyFunc func(ctx context.Context, pod *v1.Pod, reason, plugin string) error
}

func (m *mockNotifier) Name() string {
	return "mock-notifier"
}

func (m *mockNotifier) Run(_ context.Context) {
	return
}

func (m *mockNotifier) Notify(ctx context.Context, pod *v1.Pod, reason, plugin string) error {
	if m.NotifyFunc != nil {
		return m.NotifyFunc(ctx, pod, reason, plugin)
	}
	return nil
}

func TestSynchronizedPodNotifier(t *testing.T) {
	t.Parallel()

	testPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-1",
			UID:  "uid-1",
		},
	}

	notifyPod := &pluginapi.EvictPod{
		Pod:                testPod,
		Reason:             "test",
		ForceEvict:         false,
		EvictionPluginName: "test",
	}

	notifier := NewSynchronizedPodNotifier(&mockNotifier{})
	notifier.Name()
	notifier.Start(context.Background())
	err := notifier.NotifyPods(rule.RuledEvictPodList{&rule.RuledEvictPod{EvictPod: notifyPod}})
	require.NoError(t, err)
}
