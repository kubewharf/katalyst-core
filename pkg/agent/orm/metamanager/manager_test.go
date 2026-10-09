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

package metamanager

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestCanPodDeleteOnlyOnCgroupAbsence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pathErr    error
		wantDelete bool
	}{
		{
			name:       "missing cgroup",
			pathErr:    os.ErrNotExist,
			wantDelete: true,
		},
		{
			name:       "general lookup failure",
			pathErr:    errors.New("permission denied"),
			wantDelete: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			manager := &Manager{
				podFirstRemoveTime: map[string]time.Time{"pod": time.Now()},
				getPodAbsCgroupPath: func(_, _ string) (string, error) {
					return "", tc.pathErr
				},
			}

			require.Equal(t, tc.wantDelete, manager.canPodDelete("pod"))
			_, timestampRetained := manager.podFirstRemoveTime["pod"]
			require.Equal(t, !tc.wantDelete, timestampRetained)
		})
	}
}

func newTestMetaServer(pods []*v1.Pod) *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: pods},
		},
	}
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	metaServer := newTestMetaServer([]*v1.Pod{
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "pod0",
				UID:  "pod0",
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "pod1",
				UID:  "pod1",
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "pod2",
				UID:  "pod2",
			},
		},
	})

	manager := NewManager(metrics.DummyMetrics{}, func() sets.String {
		return sets.NewString("pod0", "pod3", "pod4", "pod5")
	}, metaServer)

	newPodList := make([]string, 0)
	removePodList := make([]string, 0)

	manager.RegistPodAddedFunc(func(podUID string) {
		newPodList = append(newPodList, podUID)
	})
	manager.RegistPodDeletedFunc(func(podUID string) {
		removePodList = append(removePodList, podUID)
	})

	manager.reconcile()
	require.Equal(t, 2, len(newPodList))
	require.Equal(t, 3, len(removePodList))
}

func TestReconcilePods(t *testing.T) {
	t.Parallel()

	metaServer := newTestMetaServer([]*v1.Pod{
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "pod0",
				UID:  "pod0",
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "pod1",
				UID:  "pod1",
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "pod2",
				UID:  "pod2",
			},
		},
	})

	manager := NewManager(metrics.DummyMetrics{}, func() sets.String {
		return sets.NewString("pod0", "pod3", "pod4", "pod5")
	}, metaServer)

	p := manager.GetPods()
	assert.Equal(t, 3, len(p))

	newPods, removePods, err := manager.ReconcilePods()
	assert.NoError(t, err)
	assert.Equal(t, 2, len(newPods))
	assert.Equal(t, 3, len(removePods))
}
