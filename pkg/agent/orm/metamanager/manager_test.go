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
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestMetaServer(conf *config.Configuration) (*metaserver.MetaServer, error) {
	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	if err != nil {
		return nil, err
	}

	return metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
}

func generateTestConfiguration(checkpointDir string) *config.Configuration {
	conf, _ := options.NewOptions().Config()

	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir

	return conf
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	ckDir, err := ioutil.TempDir("", "checkpoint-Test")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(ckDir) }()

	conf := generateTestConfiguration(ckDir)
	metaServer, err := generateTestMetaServer(conf)
	require.NoError(t, err)

	metaServer.PodFetcher = &pod.PodFetcherStub{
		PodList: []*v1.Pod{
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
		},
	}

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
