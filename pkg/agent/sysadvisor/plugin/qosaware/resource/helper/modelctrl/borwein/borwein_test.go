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

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	borweinutils "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	metaconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
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
)

var (
	testStrategyParam = `{
       "strategy_slots": {
           "cpu_usage_ratio": [
           {
				"slot": 5,
                "net": -1.1,
				"offset": -0.15
           },
           {
                "slot": 20,
                "net": -0.5,
				"offset": -0.08
           },
           {
                "slot": 50,
                "net": 0.2,
				"offset": 0.07
           },
           {
                "slot": 90,
                "net": 1.1,
				"offset": 0.18
           }
       ]
       },
       "strategy_special_time": {
         "cpu_usage_ratio": [
			{
				"time_range": [
					"20:30",
					"21:00"
				],
				"offset": -0.15
			},
			{
				"time_range": [
					"21:00",
					"22:00"
				],
				"offset": -0.2
			}
		]
       }
   }`
	initConf = func(t *testing.T, checkpointDir string, stateFileDir string) *config.Configuration {
		conf := generateTestConfiguration(t, checkpointDir, stateFileDir)
		conf.BorweinConfiguration.TargetIndicators = []string{
			string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio),
		}
		conf.EnableBorweinV2 = true
		dynamicConf := dynamic.NewConfiguration()
		strategyName := consts.StrategyNameBorweinV2
		dynamicConf.StrategyGroupConfiguration.EnableStrategyGroup = true
		dynamicConf.StrategyGroupConfiguration.EnabledStrategies = []configapi.Strategy{
			{
				Name: &strategyName,
				Parameters: map[string]string{
					consts.StrategyNameBorweinV2: testStrategyParam,
				},
			},
		}
		conf.DynamicAgentConfiguration.SetDynamicConfiguration(dynamicConf)
		return conf
	}
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

func TestNewBorweinController(t *testing.T) {
	t.Parallel()
	podUID := "test-pod-uid"
	podName := "test-pod"
	containerName := "test-container"
	nodeName := "node1"
	fakeCPUUsage := 20.0

	checkpointDir, err := ioutil.TempDir("", "checkpoint-TestNewBorweinController")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile-TestNewBorweinController")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	conf := generateTestConfiguration(t, checkpointDir, stateFileDir)
	conf.BorweinConfiguration.TargetIndicators = []string{
		string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio),
	}
	regionName := "test"
	regionType := configapi.QoSRegionTypeShare

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
				UID:  apitypes.UID(podUID),
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
	mc.AddContainer(podUID, containerName, &types.ContainerInfo{
		PodUID:        podUID,
		PodName:       podName,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})

	type args struct {
		regionName    string
		regionType    configapi.QoSRegionType
		ownerPoolName string
		conf          *config.Configuration
		metaReader    metacache.MetaReader
	}
	tests := []struct {
		name string
		args args
		want *BorweinController
	}{
		{
			name: "test new borwein controller normally",
			args: args{
				regionName: "test",
				regionType: regionType,
				conf:       conf,
				metaReader: mc,
			},
			want: &BorweinController{
				regionName:        regionName,
				regionType:        regionType,
				conf:              conf,
				borweinParameters: conf.BorweinConfiguration.BorweinParameters,
				indicatorOffsets: map[string]float64{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): 0,
				},
				metaReader: mc,
				indicatorOffsetUpdaters: map[string]IndicatorOffsetUpdater{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): updateCPUUsageIndicatorOffset,
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := NewBorweinController(tt.args.regionName, tt.args.regionType, tt.args.ownerPoolName, tt.args.conf, tt.args.metaReader, metrics.DummyMetrics{})
			require.True(t, reflect.DeepEqual(got.regionName, tt.want.regionName))
			require.True(t, reflect.DeepEqual(got.regionType, tt.want.regionType))
			require.True(t, reflect.DeepEqual(got.conf, tt.want.conf))
			require.True(t, reflect.DeepEqual(got.borweinParameters, tt.want.borweinParameters))
			require.True(t, reflect.DeepEqual(got.indicatorOffsets, tt.want.indicatorOffsets))
			require.True(t, reflect.DeepEqual(got.metaReader, tt.want.metaReader))
		})
	}
}

func Test_updateCPUUsageIndicatorOffset(t *testing.T) {
	t.Parallel()
	podUID1 := "test-pod-uid1"
	podName1 := "test-pod1"
	containerName := "test-container"
	podUID2 := "test-pod-uid2"
	podName2 := "test-pod2"
	nodeName := "node1"
	fakeCPUUsage := 20.0

	checkpointDir, err := ioutil.TempDir("", "checkpoint-updateCPUUsageIndicatorOffset")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile-updateCPUUsageIndicatorOffset")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	conf := initConf(t, checkpointDir, stateFileDir)

	clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}}, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetContainerMetric(podUID1, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
		store.SetContainerMetric(podUID2, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName1,
				UID:  apitypes.UID(podUID1),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerName,
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName1,
				UID:  apitypes.UID(podUID2),
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

	type args struct {
		podSet                 types.PodSet
		currentIndicatorOffset float64
		borweinParameter       *borweintypes.BorweinParameter
		metaReader             metacache.MetaReader
		conf                   *config.Configuration
		inferenceResults       *borweintypes.BorweinInferenceResults
		emitter                metrics.MetricEmitter
	}
	tests := []struct {
		name    string
		args    args
		want    float64
		wantErr bool
	}{
		{
			name: "test1",
			args: args{
				podSet: types.PodSet{
					podUID1: sets.NewString(containerName),
					podUID2: sets.NewString(containerName),
				},
				currentIndicatorOffset: 0.03,
				borweinParameter:       conf.BorweinParameters[string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio)],
				conf:                   conf,
				inferenceResults: &borweintypes.BorweinInferenceResults{
					Timestamp: 0,
					Results: map[string]map[string][]*borweininfsvc.InferenceResult{
						podUID1: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": 1.0}",
								},
							},
						},
						podUID2: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -0.5}",
								},
							},
						},
					},
				},
			},
			want:    0.18,
			wantErr: false,
		},
		{
			name: "test2",
			args: args{
				podSet: types.PodSet{
					podUID1: sets.NewString(containerName),
					podUID2: sets.NewString(containerName),
				},
				currentIndicatorOffset: 0.03,
				borweinParameter:       conf.BorweinParameters[string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio)],
				conf:                   conf,
				inferenceResults: &borweintypes.BorweinInferenceResults{
					Timestamp: 0,
					Results: map[string]map[string][]*borweininfsvc.InferenceResult{
						podUID1: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -1}",
								},
							},
						},
						podUID2: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -0.5}",
								},
							},
						},
					},
				},
			},
			want:    -0.08,
			wantErr: false,
		},
		{
			name: "test3",
			args: args{
				podSet: types.PodSet{
					podUID1: sets.NewString(containerName),
					podUID2: sets.NewString(containerName),
				},
				currentIndicatorOffset: 0.01,
				borweinParameter:       conf.BorweinParameters[string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio)],
				conf:                   conf,
				inferenceResults: &borweintypes.BorweinInferenceResults{
					Timestamp: 0,
					Results: map[string]map[string][]*borweininfsvc.InferenceResult{
						podUID1: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -1}",
								},
							},
						},
						podUID2: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -1.5}",
								},
							},
						},
					},
				},
			},
			want:    -0.15,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inferenceResultKey := borweinutils.GetInferenceResultKey(borweinconsts.ModelNameBorweinLatencyRegression)
			mc, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metaServer.MetricsFetcher)
			require.NoError(t, err)
			mc.AddContainer(podUID1, containerName, &types.ContainerInfo{
				PodUID:        podUID1,
				PodName:       podName1,
				ContainerName: containerName,
				ContainerType: v1alpha1.ContainerType_MAIN,
			})
			mc.AddContainer(podUID2, containerName, &types.ContainerInfo{
				PodUID:        podUID2,
				PodName:       podName2,
				ContainerName: containerName,
				ContainerType: v1alpha1.ContainerType_MAIN,
			})
			mc.SetInferenceResult(inferenceResultKey, tt.args.inferenceResults)
			got, err := updateCPUUsageIndicatorOffset(tt.args.podSet, tt.args.currentIndicatorOffset,
				tt.args.borweinParameter, mc, tt.args.conf, metrics.DummyMetrics{}, "test")
			if (err != nil) != tt.wantErr {
				t.Errorf("updateCPUUsageIndicatorOffset() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("updateCPUUsageIndicatorOffset() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBorweinController_updateIndicatorOffsets(t *testing.T) {
	t.Parallel()
	podUID1 := "test-pod-uid1"
	podName1 := "test-pod1"
	containerName := "test-container"
	podUID2 := "test-pod-uid2"
	podName2 := "test-pod2"
	nodeName := "node1"
	fakeCPUUsage := 20.0

	regionName := "test"
	regionType := configapi.QoSRegionTypeShare

	checkpointDir, err := ioutil.TempDir("", "checkpoint-updateCPUUsageIndicatorOffset")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile-updateCPUUsageIndicatorOffset")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	conf := initConf(t, checkpointDir, stateFileDir)

	clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}}, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetContainerMetric(podUID1, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
		store.SetContainerMetric(podUID2, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName1,
				UID:  apitypes.UID(podUID1),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerName,
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName1,
				UID:  apitypes.UID(podUID2),
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
	mc.AddContainer(podUID1, containerName, &types.ContainerInfo{
		PodUID:        podUID1,
		PodName:       podName1,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})
	mc.AddContainer(podUID2, containerName, &types.ContainerInfo{
		PodUID:        podUID2,
		PodName:       podName2,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})

	type fields struct {
		regionName              string
		regionType              configapi.QoSRegionType
		conf                    *config.Configuration
		borweinParameters       map[string]*borweintypes.BorweinParameter
		indicatorOffsets        map[string]float64
		metaReader              metacache.MetaReader
		indicatorOffsetUpdaters map[string]IndicatorOffsetUpdater
		inferenceResults        *borweintypes.BorweinInferenceResults
	}
	type args struct {
		podSet types.PodSet
	}
	tests := []struct {
		name                 string
		fields               fields
		args                 args
		wantIndicatorOffsets map[string]float64
	}{
		{
			name: "test update indicator offsets normally",
			fields: fields{
				regionName:        regionName,
				regionType:        regionType,
				conf:              conf,
				borweinParameters: conf.BorweinConfiguration.BorweinParameters,
				indicatorOffsets: map[string]float64{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): 0,
				},
				metaReader: mc,
				indicatorOffsetUpdaters: map[string]IndicatorOffsetUpdater{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): updateCPUUsageIndicatorOffset,
				},
				inferenceResults: &borweintypes.BorweinInferenceResults{
					Timestamp: 0,
					Results: map[string]map[string][]*borweininfsvc.InferenceResult{
						podUID1: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -1}",
								},
							},
						},
						podUID2: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -1.5}",
								},
							},
						},
					},
				},
			},
			args: args{
				podSet: types.PodSet{
					podUID1: sets.NewString(containerName),
					podUID2: sets.NewString(containerName),
				},
			},
			wantIndicatorOffsets: map[string]float64{
				string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): -0.15,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bc := &BorweinController{
				regionName:              tt.fields.regionName,
				regionType:              tt.fields.regionType,
				conf:                    tt.fields.conf,
				borweinParameters:       tt.fields.borweinParameters,
				indicatorOffsets:        tt.fields.indicatorOffsets,
				metaReader:              tt.fields.metaReader,
				indicatorOffsetUpdaters: tt.fields.indicatorOffsetUpdaters,
				emitter:                 metrics.DummyMetrics{},
			}
			inferenceResultKey := borweinutils.GetInferenceResultKey(borweinconsts.ModelNameBorweinLatencyRegression)
			mc.SetInferenceResult(inferenceResultKey, tt.fields.inferenceResults)
			bc.updateIndicatorOffsets(tt.args.podSet)
		})
	}
}

func TestBorweinController_getUpdatedIndicators(t *testing.T) {
	t.Parallel()
	podUID1 := "test-pod-uid1"
	podName1 := "test-pod1"
	containerName := "test-container"
	podUID2 := "test-pod-uid2"
	podName2 := "test-pod2"
	nodeName := "node1"
	fakeCPUUsage := 20.0

	regionName := "test"
	regionType := configapi.QoSRegionTypeShare

	checkpointDir, err := ioutil.TempDir("", "checkpoint-updateCPUUsageIndicatorOffset")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile-updateCPUUsageIndicatorOffset")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	conf := initConf(t, checkpointDir, stateFileDir)

	clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}}, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetContainerMetric(podUID1, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
		store.SetContainerMetric(podUID2, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName1,
				UID:  apitypes.UID(podUID1),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerName,
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName1,
				UID:  apitypes.UID(podUID2),
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
	mc.AddContainer(podUID1, containerName, &types.ContainerInfo{
		PodUID:        podUID1,
		PodName:       podName1,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})
	mc.AddContainer(podUID2, containerName, &types.ContainerInfo{
		PodUID:        podUID2,
		PodName:       podName2,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})

	type fields struct {
		regionName              string
		regionType              configapi.QoSRegionType
		conf                    *config.Configuration
		borweinParameters       map[string]*borweintypes.BorweinParameter
		indicatorOffsets        map[string]float64
		metaReader              metacache.MetaReader
		indicatorOffsetUpdaters map[string]IndicatorOffsetUpdater
	}
	type args struct {
		indicators       types.Indicator
		inferenceResults *borweintypes.BorweinInferenceResults
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   types.Indicator
	}{
		{
			name: "test get updated indicators normally",
			fields: fields{
				regionName:        regionName,
				regionType:        regionType,
				conf:              conf,
				borweinParameters: conf.BorweinConfiguration.BorweinParameters,
				indicatorOffsets: map[string]float64{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): -0.08,
				},
				metaReader: mc,
				indicatorOffsetUpdaters: map[string]IndicatorOffsetUpdater{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): updateCPUUsageIndicatorOffset,
				},
			},
			args: args{
				indicators: types.Indicator{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): types.IndicatorValue{Current: 0.45, Target: 0.7},
				},
				inferenceResults: &borweintypes.BorweinInferenceResults{
					Timestamp: 0,
					Results: map[string]map[string][]*borweininfsvc.InferenceResult{
						podUID1: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -1}",
								},
							},
						},
						podUID2: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -1.5}",
								},
							},
						},
					},
				},
			},
			want: types.Indicator{
				string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): types.IndicatorValue{Current: 0.45, Target: 0.62},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mc.SetInferenceResult(borweinconsts.ModelNameBorweinLatencyRegression, tt.args.inferenceResults)
			bc := &BorweinController{
				regionName:              tt.fields.regionName,
				regionType:              tt.fields.regionType,
				conf:                    tt.fields.conf,
				borweinParameters:       tt.fields.borweinParameters,
				indicatorOffsets:        tt.fields.indicatorOffsets,
				metaReader:              tt.fields.metaReader,
				indicatorOffsetUpdaters: tt.fields.indicatorOffsetUpdaters,
			}
			if got := bc.getUpdatedIndicators(tt.args.indicators); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BorweinController.getUpdatedIndicators() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBorweinController_GetUpdatedIndicators(t *testing.T) {
	t.Parallel()
	podUID1 := "test-pod-uid1"
	podName1 := "test-pod1"
	containerName := "test-container"
	podUID2 := "test-pod-uid2"
	podName2 := "test-pod2"
	nodeName := "node1"
	fakeCPUUsage := 20.0

	regionName := "test"
	regionType := configapi.QoSRegionTypeShare

	checkpointDir, err := ioutil.TempDir("", "checkpoint-updateCPUUsageIndicatorOffset")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile-updateCPUUsageIndicatorOffset")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	conf := initConf(t, checkpointDir, stateFileDir)

	clientSet := generateTestGenericClientSet([]runtime.Object{&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}}, nil)
	metaServer := generateTestMetaServer(clientSet)
	metaServer.NodeFetcher = node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: nodeName}, &metaconfig.NodeConfiguration{}, clientSet.KubeClient.CoreV1().Nodes())
	metaServer.MetricsFetcher.RegisterExternalMetric(func(store *metricutil.MetricStore) {
		store.SetContainerMetric(podUID1, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
		store.SetContainerMetric(podUID2, containerName, consts.MetricCPUUsageContainer, metricutil.MetricData{
			Value: fakeCPUUsage,
		})
	})
	metaServer.MetricsFetcher.Run(context.Background())
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName1,
				UID:  apitypes.UID(podUID1),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerName,
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName1,
				UID:  apitypes.UID(podUID2),
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
	mc.AddContainer(podUID1, containerName, &types.ContainerInfo{
		PodUID:        podUID1,
		PodName:       podName1,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})
	mc.AddContainer(podUID2, containerName, &types.ContainerInfo{
		PodUID:        podUID2,
		PodName:       podName2,
		ContainerName: containerName,
		ContainerType: v1alpha1.ContainerType_MAIN,
	})

	type fields struct {
		regionName              string
		regionType              configapi.QoSRegionType
		conf                    *config.Configuration
		borweinParameters       map[string]*borweintypes.BorweinParameter
		indicatorOffsets        map[string]float64
		metaReader              metacache.MetaReader
		indicatorOffsetUpdaters map[string]IndicatorOffsetUpdater
		podSet                  types.PodSet
	}
	type args struct {
		indicators       types.Indicator
		inferenceResults *borweintypes.BorweinInferenceResults
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   types.Indicator
	}{
		{
			name: "test get updated indicators normally",
			fields: fields{
				regionName:        regionName,
				regionType:        regionType,
				conf:              conf,
				borweinParameters: conf.BorweinConfiguration.BorweinParameters,
				indicatorOffsets: map[string]float64{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): -0.08,
				},
				metaReader: mc,
				indicatorOffsetUpdaters: map[string]IndicatorOffsetUpdater{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): updateCPUUsageIndicatorOffset,
				},
				podSet: types.PodSet{
					podUID1: sets.NewString(containerName),
					podUID2: sets.NewString(containerName),
				},
			},
			args: args{
				indicators: types.Indicator{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): types.IndicatorValue{Current: 0.45, Target: 0.7},
				},
				inferenceResults: &borweintypes.BorweinInferenceResults{
					Timestamp: 0,
					Results: map[string]map[string][]*borweininfsvc.InferenceResult{
						podUID1: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -1}",
								},
							},
						},
						podUID2: {
							containerName: {
								{
									InferenceType: borweininfsvc.InferenceType_Other,
									GenericOutput: "{\"predict_value\": -1.5}",
								},
							},
						},
					},
				},
			},
			want: types.Indicator{
				string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): types.IndicatorValue{Current: 0.45, Target: 0.62},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mc.SetInferenceResult(borweinconsts.ModelNameBorweinLatencyRegression, tt.args.inferenceResults)
			bc := &BorweinController{
				regionName:              tt.fields.regionName,
				regionType:              tt.fields.regionType,
				conf:                    tt.fields.conf,
				borweinParameters:       tt.fields.borweinParameters,
				indicatorOffsets:        tt.fields.indicatorOffsets,
				metaReader:              tt.fields.metaReader,
				indicatorOffsetUpdaters: tt.fields.indicatorOffsetUpdaters,
			}
			if got := bc.GetUpdatedIndicators(tt.args.indicators, tt.fields.podSet); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BorweinController.getUpdatedIndicators() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBorweinController_ResetIndicatorOffsets(t *testing.T) {
	t.Parallel()

	type fields struct {
		indicatorOffsets map[string]float64
	}
	tests := []struct {
		name    string
		fields  fields
		results map[string]float64
	}{
		{
			name: "test with nil indicatorOffsets",
		},
		{
			name: "test with non-nil indicatorOffsets",
			fields: fields{
				indicatorOffsets: map[string]float64{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): 460,
				},
			},
			results: map[string]float64{
				string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): 0,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bc := &BorweinController{
				indicatorOffsets: tt.fields.indicatorOffsets,
			}
			bc.ResetIndicatorOffsets()
			require.True(t, reflect.DeepEqual(bc.indicatorOffsets, tt.results))
		})
	}
}
