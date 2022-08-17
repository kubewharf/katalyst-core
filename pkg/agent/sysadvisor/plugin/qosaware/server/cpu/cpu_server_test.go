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
	"io/ioutil"
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)
	tmpCPUAdvisorSocketDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)
	tmpCPUPluginSocketDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir
	conf.QRMAdvisorConfiguration.CPUAdvisorSocketAbsPath = tmpCPUAdvisorSocketDir + "-cpu_advisor.sock"
	conf.QRMAdvisorConfiguration.CPUPluginSocketAbsPath = tmpCPUPluginSocketDir + "-cpu_plugin.sock"

	return conf
}

func newTestCPUServer(t *testing.T) *cpuServer {
	advisorCh := make(chan cpu.CPUProvision)
	conf := generateTestConfiguration(t)

	metaCache, err := metacache.NewMetaCache(conf, nil)
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	cpuServer, err := NewCPUServer(advisorCh, conf, metaCache, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.NotNil(t, cpuServer)

	cpuServer.getCheckpointCalled = true

	return cpuServer
}

func TestCPUServerStartAndStop(t *testing.T) {
	cs := newTestCPUServer(t)

	err := cs.Start()
	assert.NoError(t, err)

	err = cs.Stop()
	assert.NoError(t, err)
}

func TestCPUServerAddContainer(t *testing.T) {
	tests := []struct {
		name              string
		request           *cpuadvisor.AddContainerRequest
		want              *cpuadvisor.AddContainerResponse
		wantErr           bool
		wantContainerInfo *types.ContainerInfo
	}{
		{
			name: "test1",
			request: &cpuadvisor.AddContainerRequest{
				PodUid:          "testUID",
				PodNamespace:    "testPodNamespace",
				PodName:         "testPodName",
				ContainerName:   "testContainerName",
				ContainerType:   1,
				ContainerIndex:  0,
				Labels:          map[string]string{"key": "label"},
				Annotations:     map[string]string{"key": "label"},
				QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
				RequestQuantity: 1,
			},
			want:    &cpuadvisor.AddContainerResponse{},
			wantErr: false,
			wantContainerInfo: &types.ContainerInfo{
				PodUID:         "testUID",
				PodNamespace:   "testPodNamespace",
				PodName:        "testPodName",
				ContainerName:  "testContainerName",
				ContainerType:  1,
				ContainerIndex: 0,
				Labels:         map[string]string{"key": "label"},
				Annotations:    map[string]string{"key": "label"},
				QoSLevel:       consts.PodAnnotationQoSLevelSharedCores,
				CPURequest:     1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newTestCPUServer(t)
			got, err := cs.AddContainer(context.Background(), tt.request)
			if (err != nil) != tt.wantErr {
				t.Errorf("AddContainer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AddContainer() got = %v, want %v", got, tt.want)
			}

			containerInfo, ok := cs.metaCache.GetContainerInfo(tt.request.PodUid, tt.request.ContainerName)
			assert.Equal(t, ok, true)
			if !reflect.DeepEqual(containerInfo, tt.wantContainerInfo) {
				t.Errorf("AddContainer() containerInfo got = %v, want %v", containerInfo, tt.wantContainerInfo)
			}
		})
	}
}

func TestCPUServerRemovePod(t *testing.T) {
	tests := []struct {
		name    string
		request *cpuadvisor.RemovePodRequest
		want    *cpuadvisor.RemovePodResponse
		wantErr bool
	}{
		{
			name: "test1",
			request: &cpuadvisor.RemovePodRequest{
				PodUid: "testPodUID",
			},
			want:    &cpuadvisor.RemovePodResponse{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newTestCPUServer(t)
			got, err := cs.RemovePod(context.Background(), tt.request)
			if (err != nil) != tt.wantErr {
				t.Errorf("RemovePod() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RemovePod() got = %v, want %v", got, tt.want)
			}
		})
	}
}

type mockCPUServerService_ListAndWatchServer struct {
	grpc.ServerStream
	ResultsChan chan *cpuadvisor.ListAndWatchResponse
}

func (_m *mockCPUServerService_ListAndWatchServer) Send(res *cpuadvisor.ListAndWatchResponse) error {
	_m.ResultsChan <- res
	return nil
}

func TestCPUServerListAndWatch(t *testing.T) {
	tests := []struct {
		name      string
		empty     *cpuadvisor.Empty
		provision cpu.CPUProvision
		wantErr   bool
		wantRes   *cpuadvisor.ListAndWatchResponse
	}{
		{
			name:  "test1",
			empty: &cpuadvisor.Empty{},
			provision: cpu.CPUProvision{PoolSizeMap: map[string]int{
				consts.PodAnnotationQoSLevelSharedCores:    2,
				consts.PodAnnotationQoSLevelReclaimedCores: 4,
			}},
			wantErr: false,
			wantRes: &cpuadvisor.ListAndWatchResponse{
				Entries: map[string]*cpuadvisor.CalculationEntries{
					consts.PodAnnotationQoSLevelSharedCores: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: consts.PodAnnotationQoSLevelSharedCores,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
											},
										},
									},
								},
							},
						},
					},
					consts.PodAnnotationQoSLevelReclaimedCores: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: consts.PodAnnotationQoSLevelReclaimedCores,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newTestCPUServer(t)
			s := &mockCPUServerService_ListAndWatchServer{ResultsChan: make(chan *cpuadvisor.ListAndWatchResponse)}
			go func() {
				if err := cs.ListAndWatch(tt.empty, s); (err != nil) != tt.wantErr {
					t.Errorf("ListAndWatch() error = %v, wantErr %v", err, tt.wantErr)
				}
			}()
			cs.advisorCh <- tt.provision
			res := <-s.ResultsChan
			close(cs.stopCh)
			if !reflect.DeepEqual(res, tt.wantRes) {
				t.Errorf("RemovePod() got = %+v, want %+v", res, tt.wantRes)
			}
		})
	}
}
