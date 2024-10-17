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

package pod

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher/borwein/trainingtpreg"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	borweinutils "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/utils"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func TestPodAddAndRemoved(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t)
	conf.PodSyncPeriod = time.Second

	meta := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{
				PodList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							UID:  "000001",
							Name: "pod-1",
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c-1",
								},
								{
									Name: "c-2",
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							UID:  "000002",
							Name: "pod-2",
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c-1",
								},
								{
									Name: "c-2",
								},
							},
						},
						Status: v1.PodStatus{
							ContainerStatuses: []v1.ContainerStatus{
								{
									Name:  "c-1",
									Ready: true,
								},
								{
									Name:  "c-2",
									Ready: true,
								},
							},
						},
					},
				},
			},
			MetricsFetcher: metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}),
		},
	}
	timeNow := time.Now().Unix()
	res := &trainingtpreg.TrainingThroughputRegression{
		PredictValue: 0.5,
	}
	bs, _ := json.Marshal(res)
	reader := metacache.NewDummyMetaCacheImp()
	reader.SetInferenceResult(borweinutils.GetInferenceResultKey(borweinconsts.ModelNameBorweinTrainingThroughput), &borweintypes.BorweinInferenceResults{
		Timestamp: timeNow,
		Results: map[string]map[string][]*borweininfsvc.InferenceResult{
			"000002": {
				"c-1": []*borweininfsvc.InferenceResult{
					{
						GenericOutput: string(bs),
					},
				},
			},
		},
	})
	si, err := NewMetricSyncerPod(conf, struct{}{}, metrics.DummyMetrics{}, metricspool.DummyMetricsEmitterPool{}, meta, reader)
	assert.NoError(t, err)

	s := si.(*MetricSyncerPod)
	s.ctx = context.Background()

	t.Logf("run with non-empty pod fetcher")
	s.syncChanel()
	s.modelMetric()
	assert.Equal(t, len(s.rawNotifier), 1)

	metaEmpty := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{
				PodList: []*v1.Pod{},
			},
			MetricsFetcher: metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}),
		},
	}
	s.metaServer = metaEmpty

	t.Logf("reset pod fecther with empty")
	s.syncChanel()
	s.modelMetric()
	assert.Equal(t, len(s.rawNotifier), 0)
}
