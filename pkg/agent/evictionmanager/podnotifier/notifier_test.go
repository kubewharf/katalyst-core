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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

var pods = []*v1.Pod{
	{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "pod-1",
			UID:         "pod-1",
			Annotations: map[string]string{apiconsts.PodAnnotationSoftEvictNotificationKey: "true"},
		},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
		},
	},
	{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "pod-2",
			UID:         "pod-2",
			Annotations: map[string]string{apiconsts.PodAnnotationSoftEvictNotificationKey: "true"},
		},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
		},
	},
	{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-3",
			UID:  "pod-3",
		},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
		},
	},
}

func makeConf() *config.Configuration {
	conf := config.NewConfiguration()
	conf.HostPathNotifierRootPath = "/tmp/katalyst"
	os.MkdirAll(conf.HostPathNotifierRootPath, os.ModePerm)
	return conf
}

func makeMetaServer() *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: pods},
		},
	}
}

func TestHostPathPodNotifier(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset(
		&v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-1",
				UID:  "pod-1",
			},
		},
	)

	notifier, err := NewHostPathPodNotifier(makeConf(), client, makeMetaServer(), events.NewFakeRecorder(4096), metrics.DummyMetrics{})
	require.NoError(t, err)
	SyncHostPathIntv = time.Second
	notifier.Run(context.Background())
	time.Sleep(5 * SyncHostPathIntv)

	err = notifier.Notify(context.Background(), pods[0], "test", "test")
	require.NoError(t, err)

	err = notifier.Notify(context.Background(), pods[2], "test", "test")
	require.Error(t, err)
}
