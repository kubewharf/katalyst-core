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
	"fmt"

	"io/ioutil"
	"net"
	"os"
	"path"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	advisortypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	metaconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/metaserver/kcc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	testConfiguration.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	testConfiguration.MetaServerConfiguration.CheckpointManagerDir = checkpointDir

	return testConfiguration
}

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
		{
			name: "test with invalid feature name",
			args: args{
				conf: config.NewConfiguration(),
			},
			want:    "",
			wantErr: true,
		},
	}
	nowTimestamp := time.Now().Unix()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
			}}, nil)
			metaServer := generateTestMetaServer(clientSet)
			metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
			got, err := nativeGetNodeFeatureValue(nowTimestamp, tt.args.featureName, metaServer, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("NativeGetNodeFeatureValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("NativeGetNodeFeatureValue() = %v, want %v", got, tt.want)
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
		featureName   string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "test with fake container",
			args: args{
				podUID:        podUID,
				containerName: containerName,
				featureName:   consts.MetricCPUUsageContainer,
			},
			want:    fmt.Sprintf("%f", fakeCPUUsage),
			wantErr: false,
		},
		{
			name: "test with invalid feature name",
			args: args{
				podUID:        podUID,
				containerName: containerName,
			},
			want:    "",
			wantErr: true,
		},
	}
	nowTimestamp := time.Now().Unix()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nativeGetContainerFeatureValue(nowTimestamp, tt.args.podUID, tt.args.containerName,
				tt.args.featureName, metaServer,
				&metacache.MetaCacheImp{MetricsReader: metaServer.MetricsFetcher})
			if (err != nil) != tt.wantErr {
				t.Errorf("NativeGetContainerFeatureValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("NativeGetContainerFeatureValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBorweinModelResultFetcher_FetchModelResult(t *testing.T) {
	t.Parallel()
	checkpointDir, err := ioutil.TempDir("", "checkpoint-FetchModelResult")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile-FetchModelResult")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	conf := generateTestConfiguration(t, checkpointDir, stateFileDir)
	qosConfig := conf.QoSConfiguration

	podUID := "test-pod-uid"
	podName := "test-pod"
	containerName := "test-container"
	nodeName := "node1"
	fakeCPUUsage := 20.0

	clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}}, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName,
				UID:  types.UID(podUID),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerName,
					},
				},
			},
		},
	}}
	mc, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metaServer.MetricsFetcher)
	require.NoError(t, err)
	mc.AddContainer(podUID, containerName, &advisortypes.ContainerInfo{
		PodUID:        podUID,
		PodName:       podName,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})

	infSvcClient := borweininfsvc.NewInferenceServiceStubClient()
	infSvcClient.SetFakeResp(&borweininfsvc.InferenceResponse{
		PodResponseEntries: map[string]*borweininfsvc.ContainerResponseEntries{
			podUID: {
				ContainerInferenceResults: map[string]*borweininfsvc.InferenceResults{
					containerName: {
						InferenceResults: []*borweininfsvc.InferenceResult{
							{
								IsDefault: true,
							},
						},
					},
				},
			},
		},
	})

	type fields struct {
		name                  string
		qosConfig             *generic.QoSConfiguration
		nodeFeatureNames      []string
		containerFeatureNames []string
		infSvcClient          borweininfsvc.InferenceServiceClient
	}
	type args struct {
		ctx        context.Context
		metaReader metacache.MetaReader
		metaWriter metacache.MetaWriter
		metaServer *metaserver.MetaServer
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "test valid fetching",
			fields: fields{
				name:                  BorweinModelResultFetcherName,
				qosConfig:             qosConfig,
				nodeFeatureNames:      []string{NodeFeatureNodeName},
				containerFeatureNames: []string{consts.MetricCPUUsageContainer},
				infSvcClient:          infSvcClient,
			},
			args: args{
				ctx:        context.Background(),
				metaReader: mc,
				metaWriter: mc,
				metaServer: metaServer,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bmrf := &BorweinModelResultFetcher{
				name:                  tt.fields.name,
				qosConfig:             tt.fields.qosConfig,
				nodeFeatureNames:      tt.fields.nodeFeatureNames,
				containerFeatureNames: tt.fields.containerFeatureNames,
				infSvcClient:          tt.fields.infSvcClient,
				emitter:               metrics.DummyMetrics{},
			}
			if err := bmrf.FetchModelResult(tt.args.ctx, tt.args.metaReader, tt.args.metaWriter, tt.args.metaServer); (err != nil) != tt.wantErr {
				t.Errorf("BorweinModelResultFetcher.FetchModelResult() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBorweinModelResultFetcher_parseInferenceRespForPods(t *testing.T) {
	t.Parallel()
	checkpointDir, err := ioutil.TempDir("", "checkpoint-parseInferenceRespForPods")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile-parseInferenceRespForPods")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	conf := generateTestConfiguration(t, checkpointDir, stateFileDir)
	qosConfig := conf.QoSConfiguration

	podUID := "test-pod-uid"
	podName := "test-pod"
	containerName := "test-container"
	nodeName := "node1"
	fakeCPUUsage := 20.0

	clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}}, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())
	containers := []*advisortypes.ContainerInfo{
		{
			PodUID:        podUID,
			PodName:       podName,
			ContainerName: containerName,
		},
	}
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName,
				UID:  types.UID(podUID),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerName,
					},
				},
			},
		},
	}
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: pods}
	mc, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metaServer.MetricsFetcher)
	require.NoError(t, err)
	mc.AddContainer(podUID, containerName, &advisortypes.ContainerInfo{
		PodUID:        podUID,
		PodName:       podName,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})

	infSvcClient := borweininfsvc.NewInferenceServiceStubClient()
	infSvcClient.SetFakeResp(&borweininfsvc.InferenceResponse{
		PodResponseEntries: map[string]*borweininfsvc.ContainerResponseEntries{
			podUID: {
				ContainerInferenceResults: map[string]*borweininfsvc.InferenceResults{
					containerName: {
						InferenceResults: []*borweininfsvc.InferenceResult{
							{
								IsDefault: true,
							},
						},
					},
				},
			},
		},
	})

	type fields struct {
		name                  string
		qosConfig             *generic.QoSConfiguration
		nodeFeatureNames      []string
		containerFeatureNames []string
		infSvcClient          borweininfsvc.InferenceServiceClient
	}
	type args struct {
		containers []*advisortypes.ContainerInfo
		resp       *borweininfsvc.InferenceResponse
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *borweintypes.BorweinInferenceResults
		wantErr bool
	}{
		{
			name: "test normal parsing inference resp",
			fields: fields{
				name:                  BorweinModelResultFetcherName,
				qosConfig:             qosConfig,
				nodeFeatureNames:      []string{NodeFeatureNodeName},
				containerFeatureNames: []string{consts.MetricCPUUsageContainer},
				infSvcClient:          infSvcClient,
			},
			args: args{
				containers: containers,
				resp: &borweininfsvc.InferenceResponse{
					PodResponseEntries: map[string]*borweininfsvc.ContainerResponseEntries{
						podUID: {
							ContainerInferenceResults: map[string]*borweininfsvc.InferenceResults{
								containerName: {
									InferenceResults: []*borweininfsvc.InferenceResult{
										{
											IsDefault: true,
										},
									},
								},
							},
						},
					},
				},
			},
			want: &borweintypes.BorweinInferenceResults{
				Results: map[string]map[string][]*borweininfsvc.InferenceResult{
					podUID: {
						containerName: {
							{
								IsDefault: true,
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bmrf := &BorweinModelResultFetcher{
				name:                  tt.fields.name,
				qosConfig:             tt.fields.qosConfig,
				nodeFeatureNames:      tt.fields.nodeFeatureNames,
				containerFeatureNames: tt.fields.containerFeatureNames,
				infSvcClient:          tt.fields.infSvcClient,
				emitter:               metrics.DummyMetrics{},
			}
			got, err := bmrf.parseInferenceRespForPods(tt.args.containers, tt.args.resp)
			if (err != nil) != tt.wantErr {
				t.Errorf("BorweinModelResultFetcher.parseInferenceRespForPods() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			got.Timestamp = 0
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BorweinModelResultFetcher.parseInferenceRespForPods() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBorweinModelResultFetcher_getInferenceRequestForPods(t *testing.T) {
	t.Parallel()
	checkpointDir, err := ioutil.TempDir("", "checkpoint-getInferenceRequestForPods")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile-getInferenceRequestForPods")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	conf := generateTestConfiguration(t, checkpointDir, stateFileDir)
	qosConfig := conf.QoSConfiguration

	podUID := "test-pod-uid"
	podName := "test-pod"
	containerName := "test-container"
	nodeName := "node1"
	fakeCPUUsage := 20.0

	clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}}, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())
	containers := []*advisortypes.ContainerInfo{
		{
			PodUID:        podUID,
			PodName:       podName,
			ContainerName: containerName,
		},
	}
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName,
				UID:  types.UID(podUID),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerName,
					},
				},
			},
		},
	}
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: pods}
	mc, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metaServer.MetricsFetcher)
	require.NoError(t, err)
	mc.AddContainer(podUID, containerName, &advisortypes.ContainerInfo{
		PodUID:        podUID,
		PodName:       podName,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})

	infSvcClient := borweininfsvc.NewInferenceServiceStubClient()
	infSvcClient.SetFakeResp(&borweininfsvc.InferenceResponse{
		PodResponseEntries: map[string]*borweininfsvc.ContainerResponseEntries{
			podUID: {
				ContainerInferenceResults: map[string]*borweininfsvc.InferenceResults{
					containerName: {
						InferenceResults: []*borweininfsvc.InferenceResult{
							{
								IsDefault: true,
							},
						},
					},
				},
			},
		},
	})

	type fields struct {
		name                  string
		qosConfig             *generic.QoSConfiguration
		nodeFeatureNames      []string
		containerFeatureNames []string
		infSvcClient          borweininfsvc.InferenceServiceClient
	}
	type args struct {
		containers []*advisortypes.ContainerInfo
		metaServer *metaserver.MetaServer
		metaWriter metacache.MetaWriter
		metaReader metacache.MetaReader
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *borweininfsvc.InferenceRequest
		wantErr bool
	}{
		{
			name: "test normal get inference req",
			fields: fields{
				name:                  BorweinModelResultFetcherName,
				qosConfig:             qosConfig,
				nodeFeatureNames:      []string{NodeFeatureNodeName},
				containerFeatureNames: []string{consts.MetricCPUUsageContainer},
				infSvcClient:          infSvcClient,
			},
			args: args{
				containers: containers,
				metaReader: mc,
				metaWriter: mc,
				metaServer: metaServer,
			},
			want: &borweininfsvc.InferenceRequest{
				FeatureNames: []string{NodeFeatureNodeName, consts.MetricCPUUsageContainer},
				PodRequestEntries: map[string]*borweininfsvc.ContainerRequestEntries{
					podUID: {
						ContainerFeatureValues: map[string]*borweininfsvc.FeatureValues{
							containerName: {
								Values: []string{nodeName, fmt.Sprintf("%f", fakeCPUUsage)},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bmrf := &BorweinModelResultFetcher{
				name:                  tt.fields.name,
				qosConfig:             tt.fields.qosConfig,
				nodeFeatureNames:      tt.fields.nodeFeatureNames,
				containerFeatureNames: tt.fields.containerFeatureNames,
				infSvcClient:          tt.fields.infSvcClient,
				emitter:               metrics.DummyMetrics{},
			}
			got, err := bmrf.getInferenceRequestForPods(tt.args.containers, tt.args.metaReader, tt.args.metaWriter, tt.args.metaServer)
			if (err != nil) != tt.wantErr {
				t.Errorf("BorweinModelResultFetcher.getInferenceRequestForPods() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BorweinModelResultFetcher.getInferenceRequestForPods() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewBorweinModelResultFetcher(t *testing.T) {
	t.Parallel()
	checkpointDir, err := ioutil.TempDir("", "checkpoint-NewBorweinModelResultFetcher")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile-NewBorweinModelResultFetcher")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	sockDir, err := ioutil.TempDir("", "sock-NewBorweinModelResultFetcher")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(sockDir) }()

	conf := generateTestConfiguration(t, checkpointDir, stateFileDir)

	podUID := "test-pod-uid"
	podName := "test-pod"
	containerName := "test-container"
	nodeName := "node1"
	fakeCPUUsage := 20.0

	clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}}, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName,
				UID:  types.UID(podUID),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerName,
					},
				},
			},
		},
	}
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: pods}
	mc, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metaServer.MetricsFetcher)
	require.NoError(t, err)
	mc.AddContainer(podUID, containerName, &advisortypes.ContainerInfo{
		PodUID:        podUID,
		PodName:       podName,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})

	infSvcClient := borweininfsvc.NewInferenceServiceStubClient()
	infSvcClient.SetFakeResp(&borweininfsvc.InferenceResponse{
		PodResponseEntries: map[string]*borweininfsvc.ContainerResponseEntries{
			podUID: {
				ContainerInferenceResults: map[string]*borweininfsvc.InferenceResults{
					containerName: {
						InferenceResults: []*borweininfsvc.InferenceResult{
							{
								IsDefault: true,
							},
						},
					},
				},
			},
		},
	})

	type args struct {
		fetcherName                     string
		enableBorweinModelResultFetcher bool
		conf                            *config.Configuration
		extraConf                       interface{}
		emitterPool                     metricspool.MetricsEmitterPool
		metaServer                      *metaserver.MetaServer
		metaCache                       metacache.MetaCache
		inferenceServiceSocketAbsPath   string
	}
	tests := []struct {
		name    string
		args    args
		want    modelresultfetcher.ModelResultFetcher
		wantErr bool
	}{
		{
			name: "test normal new borwein model result fetcher",
			args: args{
				fetcherName:                     BorweinModelResultFetcherName,
				enableBorweinModelResultFetcher: true,
				conf:                            conf,
				emitterPool:                     metricspool.DummyMetricsEmitterPool{},
				metaServer:                      metaServer,
				metaCache:                       mc,
				inferenceServiceSocketAbsPath:   path.Join(sockDir, "test.sock"),
			},
			wantErr: false,
		},
		{
			name: "test new borwein with nil conf",
			args: args{
				fetcherName:                     BorweinModelResultFetcherName,
				enableBorweinModelResultFetcher: true,
				conf:                            nil,
				emitterPool:                     metricspool.DummyMetricsEmitterPool{},
				metaServer:                      metaServer,
				metaCache:                       mc,
			},
			wantErr: true,
		},
		{
			name: "test new borwein with nil metaServer",
			args: args{
				fetcherName:                     BorweinModelResultFetcherName,
				enableBorweinModelResultFetcher: true,
				conf:                            conf,
				emitterPool:                     metricspool.DummyMetricsEmitterPool{},
				metaServer:                      nil,
				metaCache:                       mc,
				inferenceServiceSocketAbsPath:   path.Join(sockDir, "test.sock"),
			},
			wantErr: true,
		},
		{
			name: "test new borwein with nil metacache",
			args: args{
				fetcherName:                     BorweinModelResultFetcherName,
				enableBorweinModelResultFetcher: true,
				conf:                            conf,
				emitterPool:                     metricspool.DummyMetricsEmitterPool{},
				metaServer:                      metaServer,
				metaCache:                       nil,
				inferenceServiceSocketAbsPath:   path.Join(sockDir, "test.sock"),
			},
			wantErr: true,
		},
		{
			name: "test new borwein fetcher with enableBorweinModelResultFetcher false",
			args: args{
				fetcherName:                     BorweinModelResultFetcherName,
				enableBorweinModelResultFetcher: false,
				conf:                            conf,
				emitterPool:                     metricspool.DummyMetricsEmitterPool{},
				metaServer:                      metaServer,
				metaCache:                       nil,
				inferenceServiceSocketAbsPath:   path.Join(sockDir, "test.sock"),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.args.conf != nil {
				tt.args.conf.PolicyRama.EnableBorweinModelResultFetcher = tt.args.enableBorweinModelResultFetcher
			}

			var svr *grpc.Server
			if tt.args.inferenceServiceSocketAbsPath != "" {
				svr, err = RunFakeInferenceSvr(tt.args.inferenceServiceSocketAbsPath)
				require.NoError(t, err)
				conf.BorweinConfiguration.InferenceServiceSocketAbsPath = tt.args.inferenceServiceSocketAbsPath
			}

			fetcher, err := NewBorweinModelResultFetcher(tt.args.fetcherName, tt.args.conf, tt.args.extraConf, tt.args.emitterPool, tt.args.metaServer, tt.args.metaCache)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewBorweinModelResultFetcher() error = %v, wantErr %v", err, tt.wantErr)
				if svr != nil {
					svr.Stop()
				}
				return
			} else if !tt.args.enableBorweinModelResultFetcher {
				require.Nil(t, fetcher)
				if svr != nil {
					svr.Stop()
				}
				return
			}

			if svr != nil {
				svr.Stop()
				conf.BorweinConfiguration.InferenceServiceSocketAbsPath = ""
			}
		})
	}
}

func RunFakeInferenceSvr(absSockPath string) (*grpc.Server, error) {
	lis, err := net.Listen("unix", absSockPath)
	if err != nil {
		return nil, err
	}
	s := grpc.NewServer()
	inferencesvc.RegisterInferenceServiceServer(s, &inferencesvc.UnimplementedInferenceServiceServer{})
	go func() {
		s.Serve(lis)
	}()

	_, err = process.Dial(absSockPath, 5*time.Second)
	if err != nil {
		s.Stop()
		return nil, fmt.Errorf("dial failed")
	}

	return s, nil
}
