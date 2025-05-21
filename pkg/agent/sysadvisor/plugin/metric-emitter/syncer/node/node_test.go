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

package node

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	metaconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func generateTestGenericClientSet(kubeObjects, internalObjects []runtime.Object) *client.GenericClientSet {
	return &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(kubeObjects...),
		InternalClient: internalfake.NewSimpleClientset(internalObjects...),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), internalObjects...),
	}
}

func generateTestMetaServer() (*metaserver.MetaServer, error) {
	nodeName := "test-node"

	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 4)
	if err != nil {
		return nil, err
	}

	clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}}, nil)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: cpuTopology,
			},
			MetricsFetcher: metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}),
			NodeFetcher:    node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes()),
		},
	}

	return metaServer, nil
}

func TestReceiveRawNode(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t)

	metaServer, err := generateTestMetaServer()
	assert.NoError(t, err)

	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetByStringIndex(consts.MetricCPUCodeName, "test-codename")
		store.SetByStringIndex(consts.MetricInfoIsVM, false)
	})
	metaServer.MetricsFetcher.Run(context.Background())

	si, err := NewMetricSyncerNode(conf, struct{}{}, metrics.DummyMetrics{}, metricspool.DummyMetricsEmitterPool{}, metaServer, metacache.NewDummyMetaCacheImp())
	assert.NoError(t, err)

	s := si.(*MetricSyncerNode)
	ctx, cancel := context.WithCancel(context.Background())
	rChan := make(chan metrictypes.NotifiedResponse, 20)

	go func() {
		now := time.Now()
		notifiedResponse := metrictypes.NotifiedResponse{
			Req: metrictypes.NotifiedRequest{
				MetricName: consts.MetricCPUUsageSystem,
			},
			MetricData: metricutil.MetricData{
				Value: 0.6,
				Time:  &now,
			},
		}
		rChan <- notifiedResponse
		time.Sleep(time.Second)
		cancel()
	}()

	s.receiveRawNode(ctx, rChan)
}

type testResp struct {
	tags []metrics.MetricTag
	res  float64
}

type testMetrics struct {
	res chan testResp
	metrics.DummyMetrics
}

func (t testMetrics) StoreFloat64(_ string, data float64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	t.res <- testResp{
		res:  data,
		tags: tags,
	}
	return nil
}

func TestMetricSyncerNode_receiveRawNodeNUMA(t *testing.T) {
	t.Parallel()

	testTimestamp := time.Date(2024, 11, 12, 0, 0, 0, 0, time.Local)
	testUnixTime := testTimestamp.UnixMilli()

	type fields struct {
		numaMetricMapping map[string]string
	}
	type args struct {
		resp metrictypes.NotifiedResponse
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   testResp
	}{
		{
			name: "test",
			fields: fields{
				numaMetricMapping: nodeNUMARawMetricNameMapping,
			},
			args: args{
				resp: metrictypes.NotifiedResponse{
					MetricData: metricutil.MetricData{
						Value: 100,
						Time:  &testTimestamp,
					},
					Req: metrictypes.NotifiedRequest{
						MetricName: consts.MetricMemBandwidthReadNuma,
						NumaID:     0,
					},
				},
			},
			want: testResp{
				tags: []metrics.MetricTag{
					{
						Key: "object",
						Val: "nodes",
					},
					{
						Key: "object_name",
						Val: "",
					},
					{
						Key: "timestamp",
						Val: strconv.Itoa(int(testUnixTime)),
					},
					{
						Key: "selector_numa",
						Val: "0",
					},
				},
				res: 100,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := make(chan testResp)
			defer close(res)
			n := &MetricSyncerNode{
				numaMetricMapping: tt.fields.numaMetricMapping,
				dataEmitter:       testMetrics{res: res},
				node:              &v1.Node{},
			}
			rChan := make(chan metrictypes.NotifiedResponse)
			defer close(rChan)
			go n.receiveRawNUMA(context.TODO(), rChan)
			go func() {
				rChan <- tt.args.resp
			}()
			result := <-res
			assert.Equal(t, tt.want, result)
		})
	}
}
