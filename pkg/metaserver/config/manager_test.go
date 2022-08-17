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
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
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

func generateTestKAC(evictionThreshold map[v1.ResourceName]float64) *v1alpha1.KatalystAgentConfig {
	return &v1alpha1.KatalystAgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "test-namespace",
		},
		Spec: v1alpha1.KatalystAgentConfigSpec{
			Config: v1alpha1.AgentConfig{
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
	}
}

func constructTestDynamicConfigManager(t *testing.T, nodeName string, kac *v1alpha1.KatalystAgentConfig) *DynamicConfigManager {
	clientSet := generateTestGenericClientSet(generateTestCNC(nodeName), kac)
	conf := generateTestConfiguration(t, nodeName)

	err := os.MkdirAll(testConfigCheckpointDir, os.FileMode(0755))
	require.NoError(t, err)

	checkpointManager, err := checkpointmanager.NewCheckpointManager(conf.CheckpointManagerDir)
	require.NoError(t, err)

	configLoader := NewKatalystCustomConfigLoader(clientSet, 1*time.Second, conf.NodeName)
	manager := &DynamicConfigManager{
		defaultConfig:       conf.DynamicConfiguration,
		currentConfig:       deepCopy(conf.DynamicConfiguration),
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
	kac := generateTestKAC(map[v1.ResourceName]float64{
		v1.ResourceCPU:    1.2,
		v1.ResourceMemory: 1.3,
	})
	clientSet := generateTestGenericClientSet(generateTestCNC(nodeName), kac)
	conf := generateTestConfiguration(t, nodeName)

	err := os.MkdirAll(testConfigCheckpointDir, os.FileMode(0755))
	require.NoError(t, err)

	manager, err := NewDynamicConfigManager(clientSet, &metrics.DummyMetrics{}, conf)
	require.NotNil(t, manager)

	err = manager.AddConfigWatcher(testTargetGVR)
	require.NoError(t, err)

	manager.Register(&DummyConfigurationRegister{})
	manager.UpdateConfig(context.TODO())
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
		want   *pkgconfig.DynamicConfiguration
		want1  bool
	}{
		{
			name: "test-1",
			fields: fields{
				manager: constructTestDynamicConfigManager(t, "test-node",
					generateTestKAC(map[v1.ResourceName]float64{
						v1.ResourceCPU:    1.2,
						v1.ResourceMemory: 1.3,
					})),
			},
			args: args{
				ctx: context.TODO(),
			},
			want: func() *pkgconfig.DynamicConfiguration {
				conf := generateTestConfiguration(t, "test-node")
				conf.EvictionThreshold[v1.ResourceCPU] = 1.2
				conf.EvictionThreshold[v1.ResourceMemory] = 1.3
				return conf.DynamicConfiguration
			}(),
			want1: true,
		},
		{
			name: "test-no-change",
			fields: fields{
				manager: constructTestDynamicConfigManager(t, "test-node",
					generateTestKAC(generateTestConfiguration(t, "test-node").EvictionThreshold)),
			},
			args: args{
				ctx: context.TODO(),
			},
			want: func() *pkgconfig.DynamicConfiguration {
				return generateTestConfiguration(t, "test-node").DynamicConfiguration
			}(),
			want1: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.fields.manager
			got, got1, _ := c.getConfig(tt.args.ctx)
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("getConfig() got = %+v, want %+v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("getConfig() got1 = %+v, want %+v", got1, tt.want1)
			}
		})
	}
}

func Test_applyDynamicConfig(t *testing.T) {
	type args struct {
		defaultConfig *pkgconfig.DynamicConfiguration
		currentConfig *pkgconfig.DynamicConfiguration
		dynamicConf   *dynamic.DynamicConfigCRD
	}
	tests := []struct {
		name  string
		args  args
		want  *pkgconfig.DynamicConfiguration
		want1 bool
	}{
		{
			name: "test-1",
			args: args{
				defaultConfig: func() *pkgconfig.DynamicConfiguration {
					d := pkgconfig.NewDynamicConfiguration()
					d.KubeletReadOnlyPort = 10255
					d.ReclaimedResourcesEvictionPluginConfiguration.EvictionThreshold = map[v1.ResourceName]float64{
						"cpu": 1.5,
					}
					d.MemoryPressureEvictionPluginConfiguration = &eviction.MemoryPressureEvictionPluginConfiguration{
						EnableNumaLevelDetection:             defaultEnableNumaLevelDetection,
						EnableSystemLevelDetection:           defaultEnableSystemLevelDetection,
						NumaFreeBelowWatermarkTimesThreshold: defaultNumaFreeBelowWatermarkTimesThreshold,
						SystemKswapdRateThreshold:            defaultSystemKswapdRateThreshold,
						SystemKswapdRateExceedTimesThreshold: defaultSystemKswapdRateExceedTimesThreshold,
						NumaEvictionRankingMetrics:           evictionconfig.DefaultNumaEvictionRankingMetrics,
						SystemEvictionRankingMetrics:         evictionconfig.DefaultSystemEvictionRankingMetrics,
						GracePeriod:                          evictionconfig.DefaultGracePeriod,
					}
					return d
				}(),
				currentConfig: func() *pkgconfig.DynamicConfiguration {
					d := pkgconfig.NewDynamicConfiguration()
					d.KubeletReadOnlyPort = 10255
					d.ReclaimedResourcesEvictionPluginConfiguration.EvictionThreshold = map[v1.ResourceName]float64{
						"cpu": 1.5,
					}
					d.MemoryPressureEvictionPluginConfiguration = &eviction.MemoryPressureEvictionPluginConfiguration{
						EnableNumaLevelDetection:             defaultEnableNumaLevelDetection,
						EnableSystemLevelDetection:           defaultEnableSystemLevelDetection,
						NumaFreeBelowWatermarkTimesThreshold: defaultNumaFreeBelowWatermarkTimesThreshold,
						SystemKswapdRateThreshold:            defaultSystemKswapdRateThreshold,
						SystemKswapdRateExceedTimesThreshold: defaultSystemKswapdRateExceedTimesThreshold,
						NumaEvictionRankingMetrics:           evictionconfig.DefaultNumaEvictionRankingMetrics,
						SystemEvictionRankingMetrics:         evictionconfig.DefaultSystemEvictionRankingMetrics,
						GracePeriod:                          evictionconfig.DefaultGracePeriod,
					}
					return d
				}(),
				dynamicConf: &dynamic.DynamicConfigCRD{
					KatalystAgentConfig: &v1alpha1.KatalystAgentConfig{
						Spec: v1alpha1.KatalystAgentConfigSpec{
							Config: v1alpha1.AgentConfig{
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
			want: func() *pkgconfig.DynamicConfiguration {
				d := pkgconfig.NewDynamicConfiguration()
				d.KubeletReadOnlyPort = 10255
				d.ReclaimedResourcesEvictionPluginConfiguration.EvictionThreshold = map[v1.ResourceName]float64{
					"cpu": 1.3,
				}
				d.MemoryPressureEvictionPluginConfiguration = &eviction.MemoryPressureEvictionPluginConfiguration{
					EnableNumaLevelDetection:             nonDefaultEnableNumaLevelDetection,
					EnableSystemLevelDetection:           nonDefaultEnableSystemLevelDetection,
					NumaFreeBelowWatermarkTimesThreshold: nonDefaultNumaFreeBelowWatermarkTimesThreshold,
					SystemKswapdRateThreshold:            nonDefaultSystemKswapdRateThreshold,
					SystemKswapdRateExceedTimesThreshold: nonDefaulSsystemKswapdRateExceedTimesThreshold,
					NumaEvictionRankingMetrics:           nonDefaultNumaEvictionRankingMetrics,
					SystemEvictionRankingMetrics:         nonDefaultSystemEvictionRankingMetrics,
					GracePeriod:                          nonDefaultGracePeriod,
				}
				return d
			}(),
			want1: true,
		},
		{
			name: "test-2",
			args: args{
				defaultConfig: func() *pkgconfig.DynamicConfiguration {
					d := pkgconfig.NewDynamicConfiguration()
					d.KubeletReadOnlyPort = 10255
					d.ReclaimedResourcesEvictionPluginConfiguration.EvictionThreshold = map[v1.ResourceName]float64{
						"cpu": 1.5,
					}
					d.MemoryPressureEvictionPluginConfiguration = &eviction.MemoryPressureEvictionPluginConfiguration{
						EnableNumaLevelDetection:             defaultEnableNumaLevelDetection,
						EnableSystemLevelDetection:           defaultEnableSystemLevelDetection,
						NumaFreeBelowWatermarkTimesThreshold: defaultNumaFreeBelowWatermarkTimesThreshold,
						SystemKswapdRateThreshold:            defaultSystemKswapdRateThreshold,
						SystemKswapdRateExceedTimesThreshold: defaultSystemKswapdRateExceedTimesThreshold,
						NumaEvictionRankingMetrics:           evictionconfig.DefaultNumaEvictionRankingMetrics,
						SystemEvictionRankingMetrics:         evictionconfig.DefaultSystemEvictionRankingMetrics,
						GracePeriod:                          evictionconfig.DefaultGracePeriod,
					}
					return d
				}(),
				currentConfig: func() *pkgconfig.DynamicConfiguration {
					d := pkgconfig.NewDynamicConfiguration()
					d.KubeletReadOnlyPort = 10255
					d.ReclaimedResourcesEvictionPluginConfiguration.EvictionThreshold = map[v1.ResourceName]float64{
						"cpu": 1.3,
					}
					d.MemoryPressureEvictionPluginConfiguration = &eviction.MemoryPressureEvictionPluginConfiguration{
						EnableNumaLevelDetection:             nonDefaultEnableNumaLevelDetection,
						EnableSystemLevelDetection:           nonDefaultEnableSystemLevelDetection,
						NumaFreeBelowWatermarkTimesThreshold: nonDefaultNumaFreeBelowWatermarkTimesThreshold,
						SystemKswapdRateThreshold:            nonDefaultSystemKswapdRateThreshold,
						SystemKswapdRateExceedTimesThreshold: nonDefaulSsystemKswapdRateExceedTimesThreshold,
						NumaEvictionRankingMetrics:           nonDefaultNumaEvictionRankingMetrics,
						SystemEvictionRankingMetrics:         nonDefaultSystemEvictionRankingMetrics,
						GracePeriod:                          nonDefaultGracePeriod,
					}
					return d
				}(),
				dynamicConf: &dynamic.DynamicConfigCRD{
					KatalystAgentConfig: &v1alpha1.KatalystAgentConfig{
						Spec: v1alpha1.KatalystAgentConfigSpec{
							Config: v1alpha1.AgentConfig{
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
			want: func() *pkgconfig.DynamicConfiguration {
				d := pkgconfig.NewDynamicConfiguration()
				d.KubeletReadOnlyPort = 10255
				d.ReclaimedResourcesEvictionPluginConfiguration.EvictionThreshold = map[v1.ResourceName]float64{
					"cpu": 1.3,
				}
				d.MemoryPressureEvictionPluginConfiguration = &eviction.MemoryPressureEvictionPluginConfiguration{
					EnableNumaLevelDetection:             nonDefaultEnableNumaLevelDetection,
					EnableSystemLevelDetection:           nonDefaultEnableSystemLevelDetection,
					NumaFreeBelowWatermarkTimesThreshold: nonDefaultNumaFreeBelowWatermarkTimesThreshold,
					SystemKswapdRateThreshold:            nonDefaultSystemKswapdRateThreshold,
					SystemKswapdRateExceedTimesThreshold: nonDefaulSsystemKswapdRateExceedTimesThreshold,
					NumaEvictionRankingMetrics:           nonDefaultNumaEvictionRankingMetrics,
					SystemEvictionRankingMetrics:         nonDefaultSystemEvictionRankingMetrics,
					GracePeriod:                          nonDefaultGracePeriod,
				}
				return d
			}(),
			want1: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1 := applyDynamicConfig(tt.args.defaultConfig, tt.args.currentConfig, tt.args.dynamicConf)
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("applyDynamicConfig() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("applyDynamicConfig() got1 = %v, want %v", got1, tt.want1)
			}
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
			name: "kac",
			wantGVR: schema.GroupVersionResource{
				Group:    v1alpha1.SchemeGroupVersion.Group,
				Version:  v1alpha1.SchemeGroupVersion.Version,
				Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
			},
			wantGVK: schema.GroupVersionKind{
				Group:   v1alpha1.SchemeGroupVersion.Group,
				Version: v1alpha1.SchemeGroupVersion.Version,
				Kind:    "KatalystAgentConfig",
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
		want  *dynamic.DynamicConfigCRD
		want1 bool
	}{
		{
			name: "test-1",
			args: args{
				resourceGVRMap: generateTestResourceGVRMap(),
				gvrToKind:      getGVRToGVKMap(),
				loader: generateTestLoader(toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-1",
					},
					Spec: v1alpha1.KatalystAgentConfigSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
						Config: v1alpha1.AgentConfig{
							ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
								EvictionThreshold: map[v1.ResourceName]float64{
									v1.ResourceCPU:    1.3,
									v1.ResourceMemory: 1.5,
								},
							},
						},
					},
				})),
			},
			want: &dynamic.DynamicConfigCRD{
				KatalystAgentConfig: &v1alpha1.KatalystAgentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-1",
					},
					Spec: v1alpha1.KatalystAgentConfigSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
						Config: v1alpha1.AgentConfig{
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
			want1: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kac := generateTestKAC(map[v1.ResourceName]float64{
				v1.ResourceCPU:    1.2,
				v1.ResourceMemory: 1.3,
			})
			manager := constructTestDynamicConfigManager(t, "node-name", kac)
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
		v1alpha1.ResourceNameKatalystAgentConfigs: {
			Group:    v1alpha1.SchemeGroupVersion.Group,
			Version:  v1alpha1.SchemeGroupVersion.Version,
			Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
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
