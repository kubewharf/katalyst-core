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

package server

import (
	"context"
	"io/ioutil"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

type mockMemoryServerService_ListAndWatchServer struct {
	grpc.ServerStream
	ResultsChan chan *advisorsvc.ListAndWatchResponse
}

func (_m *mockMemoryServerService_ListAndWatchServer) Send(res *advisorsvc.ListAndWatchResponse) error {
	_m.ResultsChan <- res
	return nil
}

func (_m *mockMemoryServerService_ListAndWatchServer) Context() context.Context {
	return context.TODO()
}

func generateTestMemoryAdvisorConfiguration(t *testing.T) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)
	tmpMemoryAdvisorSocketDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir
	conf.QRMAdvisorConfiguration.MemoryAdvisorSocketAbsPath = tmpMemoryAdvisorSocketDir + "-memory_advisor.sock"
	conf.QRMAdvisorConfiguration.MemoryPluginSocketAbsPath = tmpMemoryAdvisorSocketDir + "-memory_plugin.sock"

	return conf
}

func newTestMemoryServer(t *testing.T, advisor subResourceAdvisor, podList []*v1.Pod) *memoryServer {
	conf := generateTestMemoryAdvisorConfiguration(t)

	metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{
				PodList: podList,
			},
		},
	}

	memoryServer, err := NewMemoryServer(conf, &reporter.DummyHeadroomResourceManager{}, metaCache, metaServer, advisor, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.NotNil(t, memoryServer)

	return memoryServer
}

type mockQRMMemoryPluginServer struct {
	containers []*advisorsvc.ContainerMetadata
	listErr    error
}

func (c *mockQRMMemoryPluginServer) ListContainers(ctx context.Context, req *advisorsvc.Empty) (*advisorsvc.ListContainersResponse, error) {
	return &advisorsvc.ListContainersResponse{
		Containers: c.containers,
	}, c.listErr
}

func TestPopulateMetaCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		containers     []*advisorsvc.ContainerMetadata
		listErr        error
		wantContainers []*types.ContainerInfo
	}{
		{
			name: "list container normal",
			containers: []*advisorsvc.ContainerMetadata{
				{
					PodUid:        "pod1",
					PodNamespace:  "ns1",
					PodName:       "pod1",
					ContainerName: "container1",
				},
			},
			wantContainers: []*types.ContainerInfo{
				{
					PodUID:        "pod1",
					PodNamespace:  "ns1",
					PodName:       "pod1",
					ContainerName: "container1",
					RegionNames:   sets.NewString(),
				},
			},
		},
		{
			name:    "list container not implement",
			listErr: status.Errorf(codes.Unimplemented, ""),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ms := newTestMemoryServer(t, nil, []*v1.Pod{})

			qrmServer := &mockQRMMemoryPluginServer{containers: tt.containers, listErr: tt.listErr}
			server := grpc.NewServer()
			advisorsvc.RegisterQRMServiceServer(server, qrmServer)

			sock, err := net.Listen("unix", ms.pluginSocketPath)
			require.NoError(t, err)
			defer sock.Close()
			go func() {
				server.Serve(sock)
			}()

			client, conn, err := ms.createQRMClient()
			require.NoError(t, err)
			defer conn.Close()
			err = ms.populateMetaCache(client)
			assert.NoError(t, err)

			for _, ci := range tt.wantContainers {
				c, ok := ms.metaCache.GetContainerInfo(ci.PodUID, ci.ContainerName)
				assert.True(t, ok)
				assert.Equal(t, ci, c)
			}
		})
	}
}

func TestMemoryServerStartAndStop(t *testing.T) {
	t.Parallel()

	cs := newTestMemoryServer(t, nil, []*v1.Pod{})

	err := cs.Start()
	assert.NoError(t, err)

	conn, err := cs.dial(cs.advisorSocketPath, cs.period)
	assert.NoError(t, err)
	assert.NotNil(t, conn)
	_ = conn.Close()

	err = cs.Stop()
	assert.NoError(t, err)
}

type MockMemoryAdvisor struct {
	advice *types.InternalMemoryCalculationResult
	err    error
}

func (a *MockMemoryAdvisor) UpdateAndGetAdvice(_ context.Context) (interface{}, error) {
	return a.advice, a.err
}

func TestMemoryServerUpdate(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		empty      *advisorsvc.Empty
		provision  types.InternalMemoryCalculationResult
		containers []*advisorsvc.ContainerMetadata
		wantRes    *advisorsvc.ListAndWatchResponse
	}

	tests := []testCase{
		{
			name:  "normal",
			empty: &advisorsvc.Empty{},
			provision: types.InternalMemoryCalculationResult{
				TimeStamp: time.Now(),
				ContainerEntries: []types.ContainerMemoryAdvices{
					{
						PodUID:        "pod1",
						ContainerName: "c1",
						Values:        map[string]string{"k1": "v1"},
					},
					{
						PodUID:        "pod1",
						ContainerName: "c1",
						Values:        map[string]string{"k2": "v2"},
					},
					{
						PodUID:        "pod2",
						ContainerName: "c1",
						Values:        map[string]string{"k1": "v1"},
					},
					{
						PodUID:        "pod2",
						ContainerName: "c2",
						Values:        map[string]string{"k2": "v2"},
					},
				},
				ExtraEntries: []types.ExtraMemoryAdvices{
					{
						CgroupPath: "/kubepods/burstable",
						Values:     map[string]string{"k1": "v1"},
					},
					{
						CgroupPath: "/kubepods/burstable",
						Values:     map[string]string{"k2": "v2"},
					},
					{
						CgroupPath: "/kubepods/besteffort",
						Values:     map[string]string{"k1": "v1"},
					},
				},
			},
			wantRes: &advisorsvc.ListAndWatchResponse{
				PodEntries: map[string]*advisorsvc.CalculationEntries{
					"pod1": {
						ContainerEntries: map[string]*advisorsvc.CalculationInfo{
							"c1": {
								CalculationResult: &advisorsvc.CalculationResult{
									Values: map[string]string{"k1": "v1", "k2": "v2"},
								},
							},
						},
					},
					"pod2": {
						ContainerEntries: map[string]*advisorsvc.CalculationInfo{
							"c1": {
								CalculationResult: &advisorsvc.CalculationResult{
									Values: map[string]string{"k1": "v1"},
								},
							},
							"c2": {
								CalculationResult: &advisorsvc.CalculationResult{
									Values: map[string]string{"k2": "v2"},
								},
							},
						},
					},
				},
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CgroupPath: "/kubepods/burstable",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"k1": "v1", "k2": "v2"},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"k1": "v1"},
						},
					},
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"memory_numa_headroom": "{}"},
						},
					},
				},
			},
		},
	}

	testWithListAndWatch := func(
		t *testing.T,
		advisor *MockMemoryAdvisor,
		ms *memoryServer,
		tt testCase,
	) {
		qrmServer := &mockQRMMemoryPluginServer{containers: tt.containers}
		server := grpc.NewServer()
		advisorsvc.RegisterQRMServiceServer(server, qrmServer)

		sock, err := net.Listen("unix", ms.pluginSocketPath)
		require.NoError(t, err)
		defer sock.Close()
		go func() {
			server.Serve(sock)
		}()

		s := &mockMemoryServerService_ListAndWatchServer{ResultsChan: make(chan *advisorsvc.ListAndWatchResponse)}

		stop := make(chan struct{})
		go func() {
			err := ms.ListAndWatch(tt.empty, s)
			require.NoError(t, err)
			stop <- struct{}{}
		}()
		res := <-s.ResultsChan
		close(ms.stopCh)
		<-stop
		if !reflect.DeepEqual(res, tt.wantRes) {
			t.Errorf("ListAndWatch()\ngot = %+v, \nwant= %+v", res, tt.wantRes)
		}
	}

	testWithGetAdvice := func(
		t *testing.T,
		advisor *MockMemoryAdvisor,
		ms *memoryServer,
		tt testCase,
	) {
		request := &advisorsvc.GetAdviceRequest{
			Entries: map[string]*advisorsvc.ContainerMetadataEntries{},
		}
		for _, container := range tt.containers {
			if _, ok := request.Entries[container.PodUid]; !ok {
				request.Entries[container.PodUid] = &advisorsvc.ContainerMetadataEntries{}
			}
			request.Entries[container.PodUid].Entries[container.ContainerName] = container
		}

		res, err := ms.GetAdvice(context.Background(), request)
		require.NoError(t, err)
		lwResp := &advisorsvc.ListAndWatchResponse{
			PodEntries:   res.PodEntries,
			ExtraEntries: res.ExtraEntries,
		}
		require.Equal(t, tt.wantRes, lwResp)
	}

	for _, tt := range tests {
		tt := tt
		for apiMode, testFunc := range map[string]func(*testing.T, *MockMemoryAdvisor, *memoryServer, testCase){
			"ListAndWatch": testWithListAndWatch,
			"GetAdvice":    testWithGetAdvice,
		} {
			testFunc := testFunc
			t.Run(tt.name+"_"+apiMode, func(t *testing.T) {
				t.Parallel()

				advisor := &MockMemoryAdvisor{advice: &tt.provision}
				ms := newTestMemoryServer(t, advisor, []*v1.Pod{})
				testFunc(t, advisor, ms, tt)
			})
		}
	}
}
