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

package config

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/eviction"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var (
	defaultEnableNumaLevelDetection             = true
	defaultEnableSystemLevelDetection           = true
	defaultNumaFreeBelowWatermarkTimesThreshold = 4
	defaultSystemKswapdRateThreshold            = 2000
	defaultSystemKswapdRateExceedTimesThreshold = 4

	nonDefaultEnableNumaLevelDetection                   = false
	nonDefaultEnableSystemLevelDetection                 = false
	nonDefaultNumaFreeBelowWatermarkTimesThreshold       = 5
	nonDefaultSystemKswapdRateThreshold                  = 3000
	nonDefaulSsystemKswapdRateExceedTimesThreshold       = 5
	nonDefaultNumaEvictionRankingMetrics                 = []string{"metric1", "metric2"}
	nonDefaultSystemEvictionRankingMetrics               = []string{"metric3"}
	nonDefaultGracePeriod                          int64 = 30

	nonDefaultMemoryEvictionPluginConfig = v1alpha1.MemoryEvictionPluginConfig{
		EnableNumaLevelDetection:             &nonDefaultEnableNumaLevelDetection,
		EnableSystemLevelDetection:           &nonDefaultEnableSystemLevelDetection,
		NumaFreeBelowWatermarkTimesThreshold: &nonDefaultNumaFreeBelowWatermarkTimesThreshold,
		SystemKswapdRateThreshold:            &nonDefaultSystemKswapdRateThreshold,
		SystemKswapdRateExceedTimesThreshold: &nonDefaulSsystemKswapdRateExceedTimesThreshold,
		NumaEvictionRankingMetrics:           nonDefaultNumaEvictionRankingMetrics,
		SystemEvictionRankingMetrics:         nonDefaultSystemEvictionRankingMetrics,
		GracePeriod:                          &nonDefaultGracePeriod,
	}

	testConfigCheckpointDir = "/tmp/metaserver1/checkpoint1"
)

func generateTestConfiguration(t *testing.T, nodeName string) *pkgconfig.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	testConfiguration.NodeName = nodeName
	testConfiguration.CheckpointManagerDir = testConfigCheckpointDir
	return testConfiguration
}

func generateTestCNC(nodeName string) *v1alpha1.CustomNodeConfig {
	return &v1alpha1.CustomNodeConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
		Status: v1alpha1.CustomNodeConfigStatus{
			KatalystCustomConfigList: []v1alpha1.TargetConfig{
				{
					ConfigName:      "default",
					ConfigNamespace: "test-namespace",
					ConfigType:      testTargetGVR,
					Hash:            "e39c2dd73aac",
				},
			},
		},
	}
}

func generateTestEvictionConfiguration(evictionThreshold map[v1.ResourceName]float64) *v1alpha1.EvictionConfiguration {
	return &v1alpha1.EvictionConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "test-namespace",
		},
		Spec: v1alpha1.EvictionConfigurationSpec{
			Config: v1alpha1.EvictionConfig{
				EvictionPluginsConfig: v1alpha1.EvictionPluginsConfig{
					ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
						EvictionThreshold: evictionThreshold,
					},
					MemoryEvictionPluginConfig: v1alpha1.MemoryEvictionPluginConfig{
						NumaFreeBelowWatermarkTimesThreshold: &defaultNumaFreeBelowWatermarkTimesThreshold,
						SystemKswapdRateThreshold:            &defaultSystemKswapdRateThreshold,
						SystemKswapdRateExceedTimesThreshold: &defaultSystemKswapdRateExceedTimesThreshold,
						NumaEvictionRankingMetrics:           evictionconfig.DefaultNumaEvictionRankingMetrics,
						SystemEvictionRankingMetrics:         evictionconfig.DefaultSystemEvictionRankingMetrics,
					},
				},
			},
		},
	}
}

func constructTestDynamicConfigManager(t *testing.T, nodeName string, evictionConfiguration *v1alpha1.EvictionConfiguration) *DynamicConfigManager {
	clientSet := generateTestGenericClientSet(generateTestCNC(nodeName), evictionConfiguration)
	conf := generateTestConfiguration(t, nodeName)
	cncFetcher := cnc.NewCachedCNCFetcher(conf.NodeName, conf.ConfigCacheTTL,
		clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())

	err := os.MkdirAll(testConfigCheckpointDir, os.FileMode(0755))
	require.NoError(t, err)

	checkpointManager, err := checkpointmanager.NewCheckpointManager(conf.CheckpointManagerDir)
	require.NoError(t, err)

	configLoader := NewKatalystCustomConfigLoader(clientSet, 1*time.Second, cncFetcher)
	manager := &DynamicConfigManager{
		conf:                conf.AgentConfiguration,
		defaultConfig:       deepCopy(conf.GetDynamicConfiguration()),
		configLoader:        configLoader,
		emitter:             &metrics.DummyMetrics{},
		resourceGVRMap:      make(map[string]metav1.GroupVersionResource),
		checkpointGraceTime: conf.ConfigCheckpointGraceTime,
		checkpointManager:   checkpointManager,
	}

	err = manager.AddConfigWatcher(testTargetGVR)
	require.NoError(t, err)
	return manager
}

func TestNewDynamicConfigManager(t *testing.T) {
	nodeName := "test-node"
	evictionConfiguration := generateTestEvictionConfiguration(map[v1.ResourceName]float64{
		v1.ResourceCPU:    1.2,
		v1.ResourceMemory: 1.3,
	})
	clientSet := generateTestGenericClientSet(generateTestCNC(nodeName), evictionConfiguration)
	conf := generateTestConfiguration(t, nodeName)
	cncFetcher := cnc.NewCachedCNCFetcher(conf.NodeName, conf.ConfigCacheTTL,
		clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())

	err := os.MkdirAll(testConfigCheckpointDir, os.FileMode(0755))
	require.NoError(t, err)

	manager, err := NewDynamicConfigManager(clientSet, &metrics.DummyMetrics{}, cncFetcher, conf)
	require.NoError(t, err)
	require.NotNil(t, manager)

	err = manager.AddConfigWatcher(testTargetGVR)
	require.NoError(t, err)

	err = manager.InitializeConfig(context.TODO())
	require.NoError(t, err)
	go manager.Run(context.TODO())
}

func TestDynamicConfigManager_getConfig(t *testing.T) {
	type fields struct {
		manager *DynamicConfigManager
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   func(got *agent.AgentConfiguration) bool
	}{
		{
			name: "test-1",
			fields: fields{
				manager: constructTestDynamicConfigManager(t, "test-node",
					generateTestEvictionConfiguration(map[v1.ResourceName]float64{
						v1.ResourceCPU:    1.2,
						v1.ResourceMemory: 1.3,
					})),
			},
			args: args{
				ctx: context.TODO(),
			},
			want: func(got *agent.AgentConfiguration) bool {
				return got.GetDynamicConfiguration().EvictionThreshold[v1.ResourceCPU] == 1.2 &&
					got.GetDynamicConfiguration().EvictionThreshold[v1.ResourceMemory] == 1.3
			},
		},
		{
			name: "test-no-change",
			fields: fields{
				manager: constructTestDynamicConfigManager(t, "test-node",
					generateTestEvictionConfiguration(generateTestConfiguration(t, "test-node").
						GetDynamicConfiguration().EvictionThreshold)),
			},
			args: args{
				ctx: context.TODO(),
			},
			want: func(got *agent.AgentConfiguration) bool {
				return reflect.DeepEqual(got.GetDynamicConfiguration().EvictionThreshold,
					generateTestConfiguration(t, "test-node").
						GetDynamicConfiguration().EvictionThreshold)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.fields.manager
			err := c.updateConfig(tt.args.ctx)
			require.NoError(t, err)
			require.True(t, tt.want(c.conf))
		})
	}
}

func Test_applyDynamicConfig(t *testing.T) {
	type args struct {
		currentConfig *dynamic.Configuration
		dynamicConf   *crd.DynamicConfigCRD
	}
	tests := []struct {
		name string
		args args
		want func(got *dynamic.Configuration) bool
	}{
		{
			name: "test-1",
			args: args{
				currentConfig: func() *dynamic.Configuration {
					d := dynamic.NewConfiguration()
					d.EvictionThreshold = map[v1.ResourceName]float64{
						"cpu": 1.5,
					}

					d.EnableNumaLevelDetection = defaultEnableNumaLevelDetection
					d.EnableSystemLevelDetection = defaultEnableSystemLevelDetection
					d.NumaFreeBelowWatermarkTimesThreshold = defaultNumaFreeBelowWatermarkTimesThreshold
					d.SystemKswapdRateThreshold = defaultSystemKswapdRateThreshold
					d.SystemKswapdRateExceedTimesThreshold = defaultSystemKswapdRateExceedTimesThreshold
					d.NumaEvictionRankingMetrics = evictionconfig.DefaultNumaEvictionRankingMetrics
					d.SystemEvictionRankingMetrics = evictionconfig.DefaultSystemEvictionRankingMetrics
					d.GracePeriod = evictionconfig.DefaultGracePeriod
					return d
				}(),
				dynamicConf: &crd.DynamicConfigCRD{
					EvictionConfiguration: &v1alpha1.EvictionConfiguration{
						Spec: v1alpha1.EvictionConfigurationSpec{
							Config: v1alpha1.EvictionConfig{
								EvictionPluginsConfig: v1alpha1.EvictionPluginsConfig{
									ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											"cpu": 1.3,
										},
									},
									MemoryEvictionPluginConfig: nonDefaultMemoryEvictionPluginConfig,
								},
							},
						},
					},
				},
			},
			want: func(got *dynamic.Configuration) bool {
				return got.EvictionThreshold["cpu"] == 1.3 &&
					got.EnableNumaLevelDetection == nonDefaultEnableNumaLevelDetection &&
					got.EnableSystemLevelDetection == nonDefaultEnableSystemLevelDetection &&
					got.NumaFreeBelowWatermarkTimesThreshold == nonDefaultNumaFreeBelowWatermarkTimesThreshold &&
					got.SystemKswapdRateThreshold == nonDefaultSystemKswapdRateThreshold &&
					got.SystemKswapdRateExceedTimesThreshold == nonDefaulSsystemKswapdRateExceedTimesThreshold &&
					reflect.DeepEqual(got.NumaEvictionRankingMetrics, nonDefaultNumaEvictionRankingMetrics) &&
					reflect.DeepEqual(got.SystemEvictionRankingMetrics, nonDefaultSystemEvictionRankingMetrics) &&
					got.GracePeriod == nonDefaultGracePeriod
			},
		},
		{
			name: "test-2",
			args: args{
				currentConfig: func() *dynamic.Configuration {
					d := dynamic.NewConfiguration()
					d.EvictionThreshold = map[v1.ResourceName]float64{
						"cpu": 1.3,
					}

					d.EnableNumaLevelDetection = nonDefaultEnableNumaLevelDetection
					d.EnableSystemLevelDetection = nonDefaultEnableSystemLevelDetection
					d.NumaFreeBelowWatermarkTimesThreshold = nonDefaultNumaFreeBelowWatermarkTimesThreshold
					d.SystemKswapdRateThreshold = nonDefaultSystemKswapdRateThreshold
					d.SystemKswapdRateExceedTimesThreshold = nonDefaulSsystemKswapdRateExceedTimesThreshold
					d.NumaEvictionRankingMetrics = nonDefaultNumaEvictionRankingMetrics
					d.SystemEvictionRankingMetrics = nonDefaultSystemEvictionRankingMetrics
					d.GracePeriod = nonDefaultGracePeriod
					return d
				}(),
				dynamicConf: &crd.DynamicConfigCRD{
					EvictionConfiguration: &v1alpha1.EvictionConfiguration{
						Spec: v1alpha1.EvictionConfigurationSpec{
							Config: v1alpha1.EvictionConfig{
								EvictionPluginsConfig: v1alpha1.EvictionPluginsConfig{
									ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											"cpu": 1.3,
										},
									},
									MemoryEvictionPluginConfig: nonDefaultMemoryEvictionPluginConfig,
								},
							},
						},
					},
				},
			},
			want: func(got *dynamic.Configuration) bool {
				return got.EvictionThreshold["cpu"] == 1.3 &&
					got.EnableNumaLevelDetection == nonDefaultEnableNumaLevelDetection &&
					got.EnableSystemLevelDetection == nonDefaultEnableSystemLevelDetection &&
					got.NumaFreeBelowWatermarkTimesThreshold == nonDefaultNumaFreeBelowWatermarkTimesThreshold &&
					got.SystemKswapdRateThreshold == nonDefaultSystemKswapdRateThreshold &&
					got.SystemKswapdRateExceedTimesThreshold == nonDefaulSsystemKswapdRateExceedTimesThreshold &&
					reflect.DeepEqual(got.NumaEvictionRankingMetrics, nonDefaultNumaEvictionRankingMetrics) &&
					reflect.DeepEqual(got.SystemEvictionRankingMetrics, nonDefaultSystemEvictionRankingMetrics) &&
					got.GracePeriod == nonDefaultGracePeriod
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyDynamicConfig(tt.args.currentConfig, tt.args.dynamicConf)
			got := tt.args.currentConfig
			require.True(t, tt.want(got))
		})
	}
}

func Test_getGVRToKindMap(t *testing.T) {
	tests := []struct {
		name    string
		wantGVR schema.GroupVersionResource
		wantGVK schema.GroupVersionKind
	}{
		{
			name: "ec",
			wantGVR: schema.GroupVersionResource{
				Group:    v1alpha1.SchemeGroupVersion.Group,
				Version:  v1alpha1.SchemeGroupVersion.Version,
				Resource: v1alpha1.ResourceNameEvictionConfigurations,
			},
			wantGVK: schema.GroupVersionKind{
				Group:   v1alpha1.SchemeGroupVersion.Group,
				Version: v1alpha1.SchemeGroupVersion.Version,
				Kind:    "EvictionConfiguration",
			},
		},
		{
			name: "ac",
			wantGVR: schema.GroupVersionResource{
				Group:    v1alpha1.SchemeGroupVersion.Group,
				Version:  v1alpha1.SchemeGroupVersion.Version,
				Resource: v1alpha1.ResourceNameAdminQoSConfigurations,
			},
			wantGVK: schema.GroupVersionKind{
				Group:   v1alpha1.SchemeGroupVersion.Group,
				Version: v1alpha1.SchemeGroupVersion.Version,
				Kind:    "AdminQoSConfiguration",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getGVRToGVKMap(); !checkGVRToGVKMap(tt.wantGVR, tt.wantGVK, got) {
				t.Errorf("getGVRToGVKMap() = %v, wantGVR %v wantGVK %v", got, tt.wantGVR, tt.wantGVK)
			}
		})
	}
}

func checkGVRToGVKMap(gvr schema.GroupVersionResource, wantGVK schema.GroupVersionKind,
	m map[schema.GroupVersionResource]schema.GroupVersionKind) bool {
	gvk, ok := m[gvr]
	if ok && gvk == wantGVK {
		return true
	}
	return false
}

func Test_updateDynamicConf(t *testing.T) {
	type args struct {
		resourceGVRMap map[string]metav1.GroupVersionResource
		gvrToKind      map[schema.GroupVersionResource]schema.GroupVersionKind
		loader         func(gvr metav1.GroupVersionResource, conf interface{}) error
	}
	tests := []struct {
		name  string
		args  args
		want  *crd.DynamicConfigCRD
		want1 bool
	}{
		{
			name: "test-1",
			args: args{
				resourceGVRMap: generateTestResourceGVRMap(),
				gvrToKind:      getGVRToGVKMap(),
				loader: generateTestLoader(toTestUnstructured(&v1alpha1.EvictionConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-1",
					},
					Spec: v1alpha1.EvictionConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
						Config: v1alpha1.EvictionConfig{
							EvictionPluginsConfig: v1alpha1.EvictionPluginsConfig{
								ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU:    1.3,
										v1.ResourceMemory: 1.5,
									},
								},
							},
						},
					},
				})),
			},
			want: &crd.DynamicConfigCRD{
				EvictionConfiguration: &v1alpha1.EvictionConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-1",
					},
					Spec: v1alpha1.EvictionConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
						Config: v1alpha1.EvictionConfig{
							EvictionPluginsConfig: v1alpha1.EvictionPluginsConfig{
								ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU:    1.3,
										v1.ResourceMemory: 1.5,
									},
								},
							},
						},
					},
				},
			},
			want1: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ec := generateTestEvictionConfiguration(map[v1.ResourceName]float64{
				v1.ResourceCPU:    1.2,
				v1.ResourceMemory: 1.3,
			})
			manager := constructTestDynamicConfigManager(t, "node-name", ec)
			got, got1, _ := manager.updateDynamicConfig(tt.args.resourceGVRMap, tt.args.gvrToKind, tt.args.loader)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("updateDynamicConfig() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("updateDynamicConfig() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func generateTestResourceGVRMap() map[string]metav1.GroupVersionResource {
	return map[string]metav1.GroupVersionResource{
		v1alpha1.ResourceNameEvictionConfigurations: {
			Group:    v1alpha1.SchemeGroupVersion.Group,
			Version:  v1alpha1.SchemeGroupVersion.Version,
			Resource: v1alpha1.ResourceNameEvictionConfigurations,
		},
	}
}

func generateTestLoader(unstructured *unstructured.Unstructured) func(gvr metav1.GroupVersionResource, conf interface{}) error {
	return func(gvr metav1.GroupVersionResource, conf interface{}) error {
		return util.ToKCCTargetResource(unstructured).Unmarshal(conf)
	}
}

func toTestUnstructured(obj interface{}) *unstructured.Unstructured {
	ret, err := native.ToUnstructured(obj)
	if err != nil {
		klog.Error(err)
	}
	return ret
}
