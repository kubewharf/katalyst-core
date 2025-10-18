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

package borwein

import (
	"context"
	"io/ioutil"
	"os"
	"reflect"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	metaconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/metaserver/kcc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func generateTestMetaServer(clientSet *client.GenericClientSet) *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}),
		},
		ConfigurationManager: &dynamicconfig.DummyConfigurationManager{},
	}
}

func generateTestGenericClientSet(kubeObjects, internalObjects []runtime.Object) *client.GenericClientSet {
	return &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(kubeObjects...),
		InternalClient: internalfake.NewSimpleClientset(internalObjects...),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), internalObjects...),
	}
}

func TestNativeGetNodeFeatureValue(t *testing.T) {
	t.Parallel()
	nodeName := "node1"
	type args struct {
		featureName string
		conf        *config.Configuration
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "test with fake node",
			args: args{
				featureName: NodeFeatureNodeName,
				conf:        config.NewConfiguration(),
			},
			want:    "node1",
			wantErr: false,
		},
	}
	nowTimestamp := time.Now().Unix()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
			}}, nil)
			metaServer := generateTestMetaServer(clientSet)
			metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
			_, err := nativeGetNodeFeatureValue(nowTimestamp, metaServer, nil, tt.args.conf, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("NativeGetNodeFeatureValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestNativeGetContainerFeatureValue(t *testing.T) {
	t.Parallel()
	podUID := "test-pod-uid"
	containerName := "test-container"
	fakeCPUUsage := 20.0

	clientSet := generateTestGenericClientSet(nil, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())

	type args struct {
		podUID        string
		containerName string
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]interface{}
		wantErr bool
	}{
		{
			name: "test with fake container",
			args: args{
				podUID:        podUID,
				containerName: containerName,
			},
			want: map[string]interface{}{
				containerName: map[string]interface{}{
					consts.MetricCPUUsageContainer: fakeCPUUsage,
				},
			},
			wantErr: false,
		},
		{
			name: "test with not exist container",
			args: args{
				podUID:        podUID,
				containerName: "not-exist-container",
			},
			want:    nil,
			wantErr: true,
		},
	}
	nowTimestamp := time.Now().Unix()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := nativeGetContainerFeatureValue(nowTimestamp, tt.args.podUID, tt.args.containerName, metaServer,
				&metacache.MetaCacheImp{MetricsReader: metaServer.MetricsFetcher}, config.NewConfiguration(), nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("NativeGetContainerFeatureValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NativeGetContainerFeatureValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNativeGetNumaFeatureValue(t *testing.T) {
	t.Parallel()
	numaID := 0
	fakeMemBandwidth := 1000.0

	stateFileDir, err := ioutil.TempDir("", "metacache-TestNativeGetNumaFeatureValue")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(stateFileDir)
	conf := config.NewConfiguration()
	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir

	clientSet := generateTestGenericClientSet(nil, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetNumaMetric(numaID, consts.MetricMemBandwidthNuma, metricutil.MetricData{
			Value: fakeMemBandwidth,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())
	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metaServer.MetricsFetcher)
	if err != nil {
		t.Fatalf("failed to create metacache: %v", err)
	}

	type args struct {
		numaID int
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]interface{}
		wantErr bool
	}{
		{
			name: "test successful fetch",
			args: args{
				numaID: numaID,
			},
			want: map[string]interface{}{
				consts.MetricMemBandwidthNuma: fakeMemBandwidth,
			},
			wantErr: false,
		},
		{
			name: "test metric not found",
			args: args{
				numaID: 1, // different numa id
			},
			want:    nil,
			wantErr: true,
		},
	}

	nowTimestamp := time.Now().Unix()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := nativeGetNumaFeatureValue(nowTimestamp, tt.args.numaID, metaServer, metaCache, config.NewConfiguration(), nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("nativeGetNumaFeatureValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("nativeGetNumaFeatureValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetFeatureValueFuncs(t *testing.T) {
	t.Parallel()

	// backup original functions
	originalGetNodeFeatureValue := getNodeFeatureValue
	originalGetNumaFeatureValue := getNumaFeatureValue
	originalGetContainerFeatureValue := getContainerFeatureValue

	// restore original functions after test
	defer func() {
		SetGetNodeFeatureValueFunc(originalGetNodeFeatureValue)
		SetGetNumaFeatureValueFunc(originalGetNumaFeatureValue)
		SetGetContainerFeatureValueFunc(originalGetContainerFeatureValue)
	}()

	nodeCalled := false
	SetGetNodeFeatureValueFunc(func(timestamp int64, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error) {
		nodeCalled = true
		return nil, nil
	})

	numaCalled := false
	SetGetNumaFeatureValueFunc(func(timestamp int64, numaID int, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error) {
		numaCalled = true
		return nil, nil
	})

	containerCalled := false
	SetGetContainerFeatureValueFunc(func(timestamp int64, podUID string, containerName string, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration, extraConf interface{}) (map[string]interface{}, error) {
		containerCalled = true
		return nil, nil
	})

	_, _ = getNodeFeatureValue(0, nil, nil, nil, nil)
	if !nodeCalled {
		t.Errorf("getNodeFeatureValue was not called")
	}

	_, _ = getNumaFeatureValue(0, 0, nil, nil, nil, nil)
	if !numaCalled {
		t.Errorf("getNumaFeatureValue was not called")
	}

	_, _ = getContainerFeatureValue(0, "", "", nil, nil, nil, nil)
	if !containerCalled {
		t.Errorf("getContainerFeatureValue was not called")
	}
}

func TestNewBorweinModelInputFetcher(t *testing.T) {
	t.Parallel()

	newConf := func() *config.Configuration {
		conf := config.NewConfiguration()
		conf.PolicyRama.EnableBorweinModelResultFetcher = true
		return conf
	}

	type args struct {
		fetcherName string
		conf        *config.Configuration
		extraConf   interface{}
		emitterPool metricspool.MetricsEmitterPool
		metaServer  *metaserver.MetaServer
		metaCache   metacache.MetaCache
	}
	tests := []struct {
		name    string
		args    args
		wantNil bool
		wantErr bool
	}{
		{
			name: "normal case",
			args: args{
				fetcherName: "test",
				conf:        newConf(),
				emitterPool: metricspool.DummyMetricsEmitterPool{},
				metaServer:  &metaserver.MetaServer{},
			},
			wantErr: false,
		},
		{
			name: "nil conf",
			args: args{
				conf: nil,
			},
			wantErr: true,
		},
		{
			name: "nil metaserver",
			args: args{
				conf:       newConf(),
				metaServer: nil,
			},
			wantErr: true,
		},
		{
			name: "nil metacache",
			args: args{
				conf:       newConf(),
				metaServer: &metaserver.MetaServer{},
				metaCache:  nil,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.args.conf != nil {
				stateFileDir, err := ioutil.TempDir("", "metacache-TestNewBorweinModelInputFetcher")
				if err != nil {
					t.Fatalf("failed to create temp dir: %v", err)
				}
				defer os.RemoveAll(stateFileDir)
				tt.args.conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
			}

			if tt.args.metaCache == nil && tt.name != "nil metacache" && tt.args.conf != nil {
				mc, err := metacache.NewMetaCacheImp(tt.args.conf, metricspool.DummyMetricsEmitterPool{}, nil)
				if err != nil {
					t.Fatalf("failed to create metacache: %v", err)
				}
				tt.args.metaCache = mc
			}
			if tt.args.emitterPool == nil {
				tt.args.emitterPool = metricspool.DummyMetricsEmitterPool{}
			}

			got, err := NewBorweinModelInputFetcher(tt.args.fetcherName, tt.args.conf, tt.args.extraConf, tt.args.emitterPool, tt.args.metaServer, tt.args.metaCache)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewBorweinModelInputFetcher() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("NewBorweinModelInputFetcher() got = %v, want nil", got)
				}
			} else if !tt.wantErr {
				if got == nil {
					t.Errorf("NewBorweinModelInputFetcher() got nil, want non-nil")
				}
			}
		})
	}
}
