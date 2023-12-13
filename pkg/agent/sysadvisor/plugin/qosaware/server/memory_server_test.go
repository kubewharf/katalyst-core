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

func newTestMemoryServer(t *testing.T, podList []*v1.Pod) *memoryServer {
	recvCh := make(chan types.InternalMemoryCalculationResult)
	sendCh := make(chan types.TriggerInfo)
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

	memoryServer, err := NewMemoryServer(recvCh, sendCh, conf, metaCache, metaServer, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.NotNil(t, memoryServer)

	memoryServer.listAndWatchCalled = true

	return memoryServer
}

type MockQRMServiceServer struct {
	containers []*advisorsvc.ContainerMetadata
	listErr    error
}

func (ss *MockQRMServiceServer) ListContainers(context.Context, *advisorsvc.Empty) (*advisorsvc.ListContainersResponse, error) {
	return &advisorsvc.ListContainersResponse{
		Containers: ss.containers,
	}, ss.listErr
}

func TestListContainers(t *testing.T) {
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
		t.Run(tt.name, func(t *testing.T) {
			serv := grpc.NewServer()

			mockQRMServiceServer := &MockQRMServiceServer{containers: tt.containers, listErr: tt.listErr}
			advisorsvc.RegisterQRMServiceServer(serv, mockQRMServiceServer)

			ms := newTestMemoryServer(t, []*v1.Pod{})

			lis, err := net.Listen("unix", ms.pluginSocketPath)
			assert.NoError(t, err)
			defer lis.Close()
			go serv.Serve(lis)

			err = ms.Start()
			assert.NoError(t, err)

			<-ms.sendCh

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

	cs := newTestMemoryServer(t, []*v1.Pod{})

	err := cs.Start()
	assert.NoError(t, err)

	err = cs.Stop()
	assert.NoError(t, err)
}

func TestMemoryServerListAndWatch(t *testing.T) {
	t.Parallel()

	type ContainerInfo struct {
		request *advisorsvc.ContainerMetadata
	}

	tests := []struct {
		name      string
		empty     *advisorsvc.Empty
		provision types.InternalMemoryCalculationResult
		infos     []*ContainerInfo
		wantErr   bool
		wantRes   *advisorsvc.ListAndWatchResponse
	}{
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
			wantErr: false,
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
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newTestMemoryServer(t, []*v1.Pod{})
			s := &mockMemoryServerService_ListAndWatchServer{ResultsChan: make(chan *advisorsvc.ListAndWatchResponse)}
			for _, info := range tt.infos {
				assert.NoError(t, cs.addContainer(info.request))
			}
			stop := make(chan struct{})
			go func() {
				if err := cs.ListAndWatch(tt.empty, s); (err != nil) != tt.wantErr {
					t.Errorf("ListAndWatch() error = %v, wantErr %v", err, tt.wantErr)
				}
				stop <- struct{}{}
			}()
			recvCh := cs.recvCh.(chan types.InternalMemoryCalculationResult)
			recvCh <- tt.provision
			res := <-s.ResultsChan
			close(cs.stopCh)
			<-stop
			if !reflect.DeepEqual(res, tt.wantRes) {
				t.Errorf("ListAndWatch()\ngot = %+v, \nwant= %+v", res, tt.wantRes)
			}
		})
	}
}
