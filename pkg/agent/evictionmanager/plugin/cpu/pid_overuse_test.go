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

package cpu

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestPIDOveruseEvictionPluginGetEvictPods(t *testing.T) {
	t.Parallel()

	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	now := metav1.Now().Time

	setPIDMetrics := func(podUID, containerName string, runnable, uninterruptible, ioWait, sleeping float64) {
		fakeFetcher.SetContainerMetric(podUID, containerName, consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: runnable, Time: &now})
		fakeFetcher.SetContainerMetric(podUID, containerName, consts.MetricCPUNrUninterruptibleContainer, utilmetric.MetricData{Value: uninterruptible, Time: &now})
		fakeFetcher.SetContainerMetric(podUID, containerName, consts.MetricCPUNrIOWaitContainer, utilmetric.MetricData{Value: ioWait, Time: &now})
		fakeFetcher.SetContainerMetric(podUID, containerName, consts.MetricCPUNrSleepingContainer, utilmetric.MetricData{Value: sleeping, Time: &now})
	}

	setPIDMetrics("pod-1", "main", 20, 10, 5, 5)
	setPIDMetrics("pod-2", "main", 25, 10, 10, 15)
	setPIDMetrics("pod-2", "sidecar", 10, 5, 5, 5)
	setPIDMetrics("pod-3", "main", 35, 10, 5, 11)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: fakeFetcher,
		},
	}

	conf := config.NewConfiguration()
	conf.GetDynamicConfiguration().CPUPressureEvictionConfiguration.EnablePIDOveruseEviction = true
	conf.GetDynamicConfiguration().CPUPressureEvictionConfiguration.PIDOveruseThreshold = 60
	conf.GetDynamicConfiguration().CPUPressureEvictionConfiguration.PIDOveruseGracePeriod = 12

	plugin := NewPIDOveruseEvictionPlugin(nil, nil, metaServer, emitter, conf).(*PIDOveruseEvictionPlugin)
	resp, err := plugin.GetEvictPods(context.Background(), &pluginapi.GetEvictPodsRequest{
		ActivePods: []*v1.Pod{
			newPIDTestPod("pod-1", "main"),
			newPIDTestPod("pod-2", "main", "sidecar"),
			newPIDTestPod("pod-3", "main"),
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.EvictPods, 2)
	require.Equal(t, "pod-2", string(resp.EvictPods[0].Pod.UID))
	require.Equal(t, "pod-3", string(resp.EvictPods[1].Pod.UID))
	require.NotNil(t, resp.EvictPods[0].DeletionOptions)
	require.EqualValues(t, 12, resp.EvictPods[0].DeletionOptions.GracePeriodSeconds)
	require.True(t, resp.EvictPods[0].ForceEvict)
	require.Equal(t, EvictionPluginNamePIDOveruse, resp.EvictPods[0].EvictionPluginName)
}

func newPIDTestPod(uid string, containerNames ...string) *v1.Pod {
	containers := make([]v1.Container, 0, len(containerNames))
	for _, name := range containerNames {
		containers = append(containers, v1.Container{Name: name})
	}

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:  types.UID(uid),
			Name: uid,
		},
		Spec: v1.PodSpec{
			Containers: containers,
		},
	}
}
