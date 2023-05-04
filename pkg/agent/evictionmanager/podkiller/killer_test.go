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

package podkiller

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestEvictionQueue(t *testing.T) {
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-2"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-3"},
		},
	}

	ctx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{pods[0], pods[1]})
	require.NoError(t, err)

	deleteKiller := NewDeletionAPIKiller(ctx.Client.KubeClient, &events.FakeRecorder{}, metrics.DummyMetrics{})

	err = deleteKiller.Evict(context.Background(), pods[0], 0, "test-api")
	require.NoError(t, err)

	err = deleteKiller.Evict(context.Background(), pods[0], 0, "test-api")
	require.NoError(t, err)

	err = deleteKiller.Evict(context.Background(), pods[1], 0, "test-api")
	require.NoError(t, err)

	err = evict(ctx.Client.KubeClient, &events.FakeRecorder{}, metrics.DummyMetrics{}, pods[2],
		0, "test-api", func(_ *v1.Pod, gracePeriod int64) error {
			return fmt.Errorf("test")
		})
	require.ErrorContains(t, err, "test")
}
