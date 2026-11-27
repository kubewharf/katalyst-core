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

package kcc

import (
	"context"
	"fmt"
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
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var (
	defaultEnableNumaLevelDetection                      = true
	defaultEnableSystemLevelDetection                    = true
	defaultNumaFreeBelowWatermarkTimesThreshold          = 4
	defaultNumaFreeBelowWatermarkTimesReclaimedThreshold = 2
	defaultNumaFreeConstraintFastEvictionWaitCycle       = 1
	defaultSystemKswapdRateThreshold                     = 2000
	defaultSystemKswapdRateExceedDurationThreshold       = 120

	nonDefaultEnableNumaLevelEviction                             = false
	nonDefaultEnableSystemLevelEviction                           = false
	nonDefaultNumaFreeBelowWatermarkTimesThreshold                = 5
	nonDefaultNumaFreeBelowWatermarkTimesReclaimedThreshold       = 3
	nonDefaultNumaFreeConstraintFastEvictionWaitCycle             = 0
	nonDefaultSystemKswapdRateThreshold                           = 3000
	nonDefaultSystemKswapdRateExceedDurationThreshold             = 130
	nonDefaultNumaEvictionRankingMetrics                          = []string{"metric1", "metric2"}
	nonDefaultSystemEvictionRankingMetrics                        = []string{"metric3"}
	nonDefaultGracePeriod                                   int64 = 30

	nonDefaultMemoryEvictionPluginConfig = v1alpha1.MemoryPressureEvictionConfig{
		EnableNumaLevelEviction:                       &nonDefaultEnableNumaLevelEviction,
		EnableSystemLevelEviction:                     &nonDefaultEnableSystemLevelEviction,
		NumaFreeBelowWatermarkTimesThreshold:          &nonDefaultNumaFreeBelowWatermarkTimesThreshold,
		NumaFreeBelowWatermarkTimesReclaimedThreshold: &nonDefaultNumaFreeBelowWatermarkTimesReclaimedThreshold,
		NumaFreeConstraintFastEvictionWaitCycle:       &nonDefaultNumaFreeConstraintFastEvictionWaitCycle,
		SystemKswapdRateThreshold:                     &nonDefaultSystemKswapdRateThreshold,
		SystemKswapdRateExceedDurationThreshold:       &nonDefaultSystemKswapdRateExceedDurationThreshold,
		NumaEvictionRankingMetrics:                    util.ConvertStringListToNumaEvictionRankingMetrics(nonDefaultNumaEvictionRankingMetrics),
		SystemEvictionRankingMetrics:                  util.ConvertStringListToSystemEvictionRankingMetrics(nonDefaultSystemEvictionRankingMetrics),
		GracePeriod:                                   &nonDefaultGracePeriod,
	}
)

func generateTestConfiguration(t *testing.T, nodeName string, dir string) *pkgconfig.Configuration {
	testConfiguration := pkgconfig.NewConfiguration()
	require.NotNil(t, testConfiguration)

	testConfiguration.NodeName = nodeName
	testConfiguration.CheckpointManagerDir = dir
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

func generateTestEvictionConfiguration(evictionThreshold map[v1.ResourceName]float64) *v1alpha1.AdminQoSConfiguration {
	return &v1alpha1.AdminQoSConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "test-namespace",
		},
		Spec: v1alpha1.AdminQoSConfigurationSpec{
			Config: v1alpha1.AdminQoSConfig{
				EvictionConfig: &v1alpha1.EvictionConfig{
					ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
						EvictionThreshold: evictionThreshold,
					},
					MemoryPressureEvictionConfig: &v1alpha1.MemoryPressureEvictionConfig{
						NumaFreeBelowWatermarkTimesThreshold:    &defaultNumaFreeBelowWatermarkTimesThreshold,
						SystemKswapdRateThreshold:               &defaultSystemKswapdRateThreshold,
						SystemKswapdRateExceedDurationThreshold: &defaultSystemKswapdRateExceedDurationThreshold,
						NumaEvictionRankingMetrics:              util.ConvertStringListToNumaEvictionRankingMetrics(evictionconfig.DefaultNumaEvictionRankingMetrics),
						SystemEvictionRankingMetrics:            util.ConvertStringListToSystemEvictionRankingMetrics(evictionconfig.DefaultSystemEvictionRankingMetrics),
					},
				},
			},
		},
	}
}

func constructTestDynamicConfigManager(t *testing.T, nodeName, dir string, adminQoSConfiguration *v1alpha1.AdminQoSConfiguration) *DynamicConfigManager {
	clientSet := generateTestGenericClientSet(generateTestCNC(nodeName), adminQoSConfiguration)
	conf := generateTestConfiguration(t, nodeName, dir)
	cncFetcher := cnc.NewCachedCNCFetcher(conf.BaseConfiguration, conf.CNCConfiguration,
		clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())

	err := os.MkdirAll(dir, os.FileMode(0o755))
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
	t.Parallel()

	nodeName := "test-node"
	evictionConfiguration := generateTestEvictionConfiguration(map[v1.ResourceName]float64{
		v1.ResourceCPU:    1.2,
		v1.ResourceMemory: 1.3,
	})
	clientSet := generateTestGenericClientSet(generateTestCNC(nodeName), evictionConfiguration)
	conf := generateTestConfiguration(t, nodeName, "/tmp/metaserver1/TestNewDynamicConfigManager")
	cncFetcher := cnc.NewCachedCNCFetcher(conf.BaseConfiguration, conf.CNCConfiguration,
		clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())
	defer os.RemoveAll(conf.CheckpointManagerDir)

	err := os.MkdirAll("/tmp/metaserver1/TestNewDynamicConfigManager", os.FileMode(0o755))
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

func TestDynamicConfigManager_ConfigHooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		hooks       []ConfigurationHook
		wantErr     bool
		execOrder   []int
		description string
	}{
		{
			name: "single hook success",
			hooks: []ConfigurationHook{
				func(ctx context.Context, oldConf, newConf *dynamic.Configuration) error {
					return nil
				},
			},
			wantErr:   false,
			execOrder: []int{0},
		},
		{
			name: "multiple hooks success",
			hooks: []ConfigurationHook{
				func(ctx context.Context, oldConf, newConf *dynamic.Configuration) error {
					return nil
				},
				func(ctx context.Context, oldConf, newConf *dynamic.Configuration) error {
					return nil
				},
			},
			wantErr:   false,
			execOrder: []int{0, 1},
		},
		{
			name: "hook failure",
			hooks: []ConfigurationHook{
				func(ctx context.Context, oldConf, newConf *dynamic.Configuration) error {
					return fmt.Errorf("hook failed")
				},
			},
			wantErr: true,
		},
		{
			name: "partial hooks failure",
			hooks: []ConfigurationHook{
				func(ctx context.Context, oldConf, newConf *dynamic.Configuration) error {
					return nil
				},
				func(ctx context.Context, oldConf, newConf *dynamic.Configuration) error {
					return fmt.Errorf("second hook failed")
				},
			},
			wantErr:   true,
			execOrder: []int{0},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			aqc := generateTestEvictionConfiguration(map[v1.ResourceName]float64{
				v1.ResourceCPU:    1.2,
				v1.ResourceMemory: 1.3,
			})
			manager := constructTestDynamicConfigManager(t, "test-node",
				"/tmp/metaserver1/TestConfigHooks", aqc)
			manager.AddConfigHook(tt.hooks...)

			// Setup test data
			oldConf := &dynamic.Configuration{}
			manager.conf.SetDynamicConfiguration(oldConf)

			// Execute updateConfig which triggers hooks
			err := manager.updateConfig(context.Background())

			if (err != nil) != tt.wantErr {
				t.Errorf("updateConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDynamicConfigManager_getConfig(t *testing.T) {
	t.Parallel()

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
				manager: constructTestDynamicConfigManager(t, "test-node", "/tmp/metaserver1/TestDynamicConfigManager_test-node",
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
				manager: constructTestDynamicConfigManager(t, "test-node", "/tmp/metaserver1/TestDynamicConfigManager_test-no-change",
					generateTestEvictionConfiguration(generateTestConfiguration(t, "test-node", "/tmp/metaserver1/TestDynamicConfigManager_test-no-change").
						GetDynamicConfiguration().EvictionThreshold)),
			},
			args: args{
				ctx: context.TODO(),
			},
			want: func(got *agent.AgentConfiguration) bool {
				return reflect.DeepEqual(got.GetDynamicConfiguration().EvictionThreshold,
					generateTestConfiguration(t, "test-node", "/tmp/metaserver1/TestDynamicConfigManager_test-no-change").
						GetDynamicConfiguration().EvictionThreshold)
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tt.fields.manager
			defer os.RemoveAll(c.conf.CheckpointManagerDir)
			err := c.updateConfig(tt.args.ctx)
			require.NoError(t, err)
			require.True(t, tt.want(c.conf))
		})
	}
}

func Test_applyDynamicConfig(t *testing.T) {
	t.Parallel()

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

					d.EnableNumaLevelEviction = defaultEnableNumaLevelDetection
					d.EnableSystemLevelEviction = defaultEnableSystemLevelDetection
					d.NumaFreeBelowWatermarkTimesThreshold = defaultNumaFreeBelowWatermarkTimesThreshold
					d.NumaFreeBelowWatermarkTimesReclaimedThreshold = defaultNumaFreeBelowWatermarkTimesReclaimedThreshold
					d.NumaFreeConstraintFastEvictionWaitCycle = defaultNumaFreeConstraintFastEvictionWaitCycle
					d.SystemKswapdRateThreshold = defaultSystemKswapdRateThreshold
					d.SystemKswapdRateExceedDurationThreshold = defaultSystemKswapdRateExceedDurationThreshold
					d.NumaEvictionRankingMetrics = evictionconfig.DefaultNumaEvictionRankingMetrics
					d.SystemEvictionRankingMetrics = evictionconfig.DefaultSystemEvictionRankingMetrics
					d.MemoryPressureEvictionConfiguration.GracePeriod = evictionconfig.DefaultGracePeriod
					return d
				}(),
				dynamicConf: &crd.DynamicConfigCRD{
					AdminQoSConfiguration: &v1alpha1.AdminQoSConfiguration{
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											"cpu": 1.3,
										},
									},
									MemoryPressureEvictionConfig: &nonDefaultMemoryEvictionPluginConfig,
								},
							},
						},
					},
				},
			},
			want: func(got *dynamic.Configuration) bool {
				return got.EvictionThreshold["cpu"] == 1.3 &&
					got.EnableNumaLevelEviction == nonDefaultEnableNumaLevelEviction &&
					got.EnableSystemLevelEviction == nonDefaultEnableSystemLevelEviction &&
					got.NumaFreeBelowWatermarkTimesThreshold == nonDefaultNumaFreeBelowWatermarkTimesThreshold &&
					got.NumaFreeBelowWatermarkTimesReclaimedThreshold == nonDefaultNumaFreeBelowWatermarkTimesReclaimedThreshold &&
					got.NumaFreeConstraintFastEvictionWaitCycle == nonDefaultNumaFreeConstraintFastEvictionWaitCycle &&
					got.SystemKswapdRateThreshold == nonDefaultSystemKswapdRateThreshold &&
					got.SystemKswapdRateExceedDurationThreshold == nonDefaultSystemKswapdRateExceedDurationThreshold &&
					reflect.DeepEqual(got.NumaEvictionRankingMetrics, nonDefaultNumaEvictionRankingMetrics) &&
					reflect.DeepEqual(got.SystemEvictionRankingMetrics, nonDefaultSystemEvictionRankingMetrics) &&
					got.MemoryPressureEvictionConfiguration.GracePeriod == nonDefaultGracePeriod
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

					d.EnableNumaLevelEviction = nonDefaultEnableNumaLevelEviction
					d.EnableSystemLevelEviction = nonDefaultEnableSystemLevelEviction
					d.NumaFreeBelowWatermarkTimesThreshold = nonDefaultNumaFreeBelowWatermarkTimesThreshold
					d.NumaFreeBelowWatermarkTimesReclaimedThreshold = nonDefaultNumaFreeBelowWatermarkTimesReclaimedThreshold
					d.NumaFreeConstraintFastEvictionWaitCycle = nonDefaultNumaFreeConstraintFastEvictionWaitCycle
					d.SystemKswapdRateThreshold = nonDefaultSystemKswapdRateThreshold
					d.SystemKswapdRateExceedDurationThreshold = nonDefaultSystemKswapdRateExceedDurationThreshold
					d.NumaEvictionRankingMetrics = nonDefaultNumaEvictionRankingMetrics
					d.SystemEvictionRankingMetrics = nonDefaultSystemEvictionRankingMetrics
					d.MemoryPressureEvictionConfiguration.GracePeriod = nonDefaultGracePeriod
					return d
				}(),
				dynamicConf: &crd.DynamicConfigCRD{
					AdminQoSConfiguration: &v1alpha1.AdminQoSConfiguration{
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											"cpu": 1.3,
										},
									},
									MemoryPressureEvictionConfig: &nonDefaultMemoryEvictionPluginConfig,
								},
							},
						},
					},
				},
			},
			want: func(got *dynamic.Configuration) bool {
				return got.EvictionThreshold["cpu"] == 1.3 &&
					got.EnableNumaLevelEviction == nonDefaultEnableNumaLevelEviction &&
					got.EnableSystemLevelEviction == nonDefaultEnableSystemLevelEviction &&
					got.NumaFreeBelowWatermarkTimesThreshold == nonDefaultNumaFreeBelowWatermarkTimesThreshold &&
					got.NumaFreeBelowWatermarkTimesReclaimedThreshold == nonDefaultNumaFreeBelowWatermarkTimesReclaimedThreshold &&
					got.NumaFreeConstraintFastEvictionWaitCycle == nonDefaultNumaFreeConstraintFastEvictionWaitCycle &&
					got.SystemKswapdRateThreshold == nonDefaultSystemKswapdRateThreshold &&
					got.SystemKswapdRateExceedDurationThreshold == nonDefaultSystemKswapdRateExceedDurationThreshold &&
					reflect.DeepEqual(got.NumaEvictionRankingMetrics, nonDefaultNumaEvictionRankingMetrics) &&
					reflect.DeepEqual(got.SystemEvictionRankingMetrics, nonDefaultSystemEvictionRankingMetrics) &&
					got.MemoryPressureEvictionConfiguration.GracePeriod == nonDefaultGracePeriod
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, applyDynamicConfig(tt.args.currentConfig, tt.args.dynamicConf))
			got := tt.args.currentConfig
			require.True(t, tt.want(got))
		})
	}
}

func TestDynamicConfigManager_updateConfigFragMemDefragPolicy(t *testing.T) {
	t.Parallel()

	for _, startupPolicy := range []v1alpha1.THPDefragPolicy{"", v1alpha1.THPDefragPolicyMadvise} {
		t.Run(fmt.Sprintf("startup policy %q", startupPolicy), func(t *testing.T) {
			testDynamicConfigManagerFragMemDefragPolicy(t, startupPolicy)
		})
	}
}

func testDynamicConfigManagerFragMemDefragPolicy(t *testing.T, startupPolicy v1alpha1.THPDefragPolicy) {
	t.Helper()

	nodeName := "test-node"
	dir := t.TempDir()
	enableFragMem := true

	adminQoSConfiguration := &v1alpha1.AdminQoSConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "test-namespace",
		},
		Spec: v1alpha1.AdminQoSConfigurationSpec{
			Config: v1alpha1.AdminQoSConfig{
				QRMPluginConfig: &v1alpha1.QRMPluginConfig{
					MemoryPluginConfig: &v1alpha1.MemoryPluginConfig{
						FragMemConfig: &v1alpha1.FragMemConfig{
							EnableFragMem: &enableFragMem,
						},
					},
				},
			},
		},
	}

	clientSet := generateTestGenericClientSet(generateTestCNC(nodeName), adminQoSConfiguration)
	conf := generateTestConfiguration(t, nodeName, dir)
	conf.GetDynamicConfiguration().FragMemConfiguration.EnableFragMem = false
	conf.GetDynamicConfiguration().FragMemConfiguration.MemFragScoreAsync = 80
	conf.GetDynamicConfiguration().FragMemConfiguration.THPDefaultConfig = v1alpha1.THPModeMadvise
	conf.GetDynamicConfiguration().FragMemConfiguration.THPHighOrderScoreThreshold = 85
	conf.GetDynamicConfiguration().FragMemConfiguration.THPDefragPolicy = startupPolicy

	cncFetcher := cnc.NewCachedCNCFetcher(conf.BaseConfiguration, conf.CNCConfiguration,
		clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())
	checkpointManager, err := checkpointmanager.NewCheckpointManager(conf.CheckpointManagerDir)
	require.NoError(t, err)

	manager := &DynamicConfigManager{
		conf:                conf.AgentConfiguration,
		defaultConfig:       deepCopy(conf.GetDynamicConfiguration()),
		configLoader:        NewKatalystCustomConfigLoader(clientSet, 1*time.Second, cncFetcher),
		emitter:             &metrics.DummyMetrics{},
		resourceGVRMap:      make(map[string]metav1.GroupVersionResource),
		checkpointGraceTime: conf.ConfigCheckpointGraceTime,
		checkpointManager:   checkpointManager,
	}
	require.NoError(t, manager.AddConfigWatcher(testTargetGVR))
	require.NoError(t, manager.updateConfig(context.TODO()))

	got := manager.conf.GetDynamicConfiguration().FragMemConfiguration
	require.NotNil(t, got)
	require.True(t, got.EnableFragMem)
	require.Equal(t, 80, got.MemFragScoreAsync)
	require.Equal(t, v1alpha1.THPModeMadvise, got.THPDefaultConfig)
	require.Equal(t, 85, got.THPHighOrderScoreThreshold)
	require.Equal(t, startupPolicy, got.THPDefragPolicy)

	hookCalls := 0
	manager.AddConfigHook(func(_ context.Context, _, _ *dynamic.Configuration) error {
		hookCalls++
		return nil
	})
	tests := []struct {
		name          string
		policy        v1alpha1.THPDefragPolicy
		omitPolicy    bool
		removeFragMem bool
		disable       bool
		want          v1alpha1.THPDefragPolicy
		wantErr       bool
	}{
		{name: "always overrides CLI", policy: v1alpha1.THPDefragPolicyAlways, want: v1alpha1.THPDefragPolicyAlways},
		{name: "defer overrides CLI", policy: v1alpha1.THPDefragPolicyDefer, want: v1alpha1.THPDefragPolicyDefer},
		{name: "defer madvise overrides CLI", policy: v1alpha1.THPDefragPolicyDeferMadvise, want: v1alpha1.THPDefragPolicyDeferMadvise},
		{name: "madvise", policy: v1alpha1.THPDefragPolicyMadvise, want: v1alpha1.THPDefragPolicyMadvise},
		{name: "never", policy: v1alpha1.THPDefragPolicyNever, want: v1alpha1.THPDefragPolicyNever},
		{name: "invalid policy rejects entire update", policy: "force", disable: true, wantErr: true},
		{name: "empty explicit policy rejected", policy: "", wantErr: true},
		{name: "AQC enum is case sensitive", policy: "Always", wantErr: true},
		{name: "AQC enum rejects whitespace", policy: " defer ", wantErr: true},
		{name: "omitted policy restores CLI", omitPolicy: true, want: startupPolicy},
		{name: "policy can update while FragMem disabled", policy: v1alpha1.THPDefragPolicyNever, disable: true, want: v1alpha1.THPDefragPolicyNever},
		{name: "removing FragMem restores CLI", removeFragMem: true, want: startupPolicy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := adminQoSConfiguration.DeepCopy()
			fragMem := next.Spec.Config.QRMPluginConfig.MemoryPluginConfig.FragMemConfig
			enabled := !tt.disable
			fragMem.EnableFragMem = &enabled
			if !tt.omitPolicy {
				fragMem.THPDefragPolicy = &tt.policy
			}
			if tt.removeFragMem {
				next.Spec.Config.QRMPluginConfig.MemoryPluginConfig.FragMemConfig = nil
			}
			_, err := clientSet.DynamicClient.Resource(schema.GroupVersionResource(testTargetGVR)).
				Namespace(next.Namespace).Update(context.Background(), toTestUnstructured(next), metav1.UpdateOptions{})
			require.NoError(t, err)
			// Start each load without a cached CNC hash so the updated JSON is decoded.
			manager.configLoader = NewKatalystCustomConfigLoader(clientSet, 0, cncFetcher)
			previousConfig := manager.conf.GetDynamicConfiguration()
			previousCRD := manager.lastDynamicConfigCRD
			previousHookCalls := hookCalls

			err = manager.updateConfig(context.Background())
			if tt.wantErr {
				require.ErrorContains(t, err, "invalid THP defrag policy")
				require.Same(t, previousConfig, manager.conf.GetDynamicConfiguration())
				require.Same(t, previousCRD, manager.lastDynamicConfigCRD)
				require.Equal(t, previousHookCalls, hookCalls, "invalid input must be rejected before hooks run")
				return
			}
			require.NoError(t, err)
			got := manager.conf.GetDynamicConfiguration().FragMemConfiguration
			require.Equal(t, tt.want, got.THPDefragPolicy)
			require.Equal(t, enabled && !tt.removeFragMem, got.EnableFragMem)
			require.Equal(t, 80, got.MemFragScoreAsync)
			require.Equal(t, v1alpha1.THPModeMadvise, got.THPDefaultConfig)
			require.Equal(t, 85, got.THPHighOrderScoreThreshold)
			require.Nil(t, got.THPStaticEnableConfig)
			require.Equal(t, startupPolicy, manager.defaultConfig.FragMemConfiguration.THPDefragPolicy)
		})
	}
}

func TestDynamicConfigManager_updateConfigVMCompactionProactiveness(t *testing.T) {
	t.Parallel()

	zero, twenty, hundred := int64(0), int64(20), int64(100)
	negative, excessive := int64(-1), int64(101)
	for _, startup := range []*int64{nil, &zero, &twenty} {
		name := "unset"
		if startup != nil {
			name = fmt.Sprint(*startup)
		}
		t.Run("startup "+name, func(t *testing.T) {
			aqc := generateTestEvictionConfiguration(nil)
			// A valid THP policy must not bypass proactiveness validation.
			policy := v1alpha1.THPDefragPolicyNever
			aqc.Spec.Config.QRMPluginConfig = &v1alpha1.QRMPluginConfig{
				MemoryPluginConfig: &v1alpha1.MemoryPluginConfig{
					FragMemConfig: &v1alpha1.FragMemConfig{THPDefragPolicy: &policy},
				},
			}
			manager := constructTestDynamicConfigManager(t, "test-node", t.TempDir(), aqc)
			manager.conf.GetDynamicConfiguration().FragMemConfiguration.VMCompactionProactiveness = startup
			manager.defaultConfig = deepCopy(manager.conf.GetDynamicConfiguration())
			loader := manager.configLoader.(*katalystCustomConfigLoader)
			require.NoError(t, manager.updateConfig(context.Background()))
			require.Equal(t, startup, manager.conf.GetDynamicConfiguration().FragMemConfiguration.VMCompactionProactiveness)

			hookCalls := 0
			manager.AddConfigHook(func(_ context.Context, _, _ *dynamic.Configuration) error {
				hookCalls++
				return nil
			})
			for _, tc := range []struct {
				name      string
				value     *int64
				remove    string
				want      *int64
				wantErr   string
				enable    bool
				thpPolicy v1alpha1.THPDefragPolicy
			}{
				{name: "zero overrides startup while disabled", value: &zero, want: &zero},
				{name: "maximum while enabled", value: &hundred, want: &hundred, enable: true},
				{name: "negative rejects entire update", value: &negative, wantErr: "invalid vm.compaction_proactiveness"},
				{name: "excessive rejects entire update", value: &excessive, wantErr: "invalid vm.compaction_proactiveness"},
				{name: "invalid THP policy rejects valid target", value: &twenty, thpPolicy: "force", wantErr: "invalid THP defrag policy"},
				{name: "omission restores startup", want: startup},
				{name: "new AQC target", value: &hundred, want: &hundred},
				{name: "removing FragMem restores startup", remove: "fragmem", want: startup},
				{name: "zero again", value: &zero, want: &zero},
				{name: "removing memory configuration restores startup", remove: "memory", want: startup},
				{name: "maximum again", value: &hundred, want: &hundred},
				{name: "removing QRM configuration restores startup", remove: "qrm", want: startup},
			} {
				t.Run(tc.name, func(t *testing.T) {
					next := aqc.DeepCopy()
					fragMem := next.Spec.Config.QRMPluginConfig.MemoryPluginConfig.FragMemConfig
					fragMem.VMCompactionProactiveness = tc.value
					fragMem.EnableFragMem = &tc.enable
					if tc.thpPolicy != "" {
						fragMem.THPDefragPolicy = &tc.thpPolicy
					}
					switch tc.remove {
					case "fragmem":
						next.Spec.Config.QRMPluginConfig.MemoryPluginConfig.FragMemConfig = nil
					case "memory":
						next.Spec.Config.QRMPluginConfig.MemoryPluginConfig = nil
					case "qrm":
						next.Spec.Config.QRMPluginConfig = nil
					}
					_, err := loader.client.DynamicClient.Resource(schema.GroupVersionResource(testTargetGVR)).
						Namespace(next.Namespace).Update(context.Background(), toTestUnstructured(next), metav1.UpdateOptions{})
					require.NoError(t, err)
					// Reload changed JSON without reusing the CNC hash cache.
					manager.configLoader = NewKatalystCustomConfigLoader(loader.client, 0, loader.cncFetcher)
					previous := manager.conf.GetDynamicConfiguration()
					previousCRD := manager.lastDynamicConfigCRD
					previousHookCalls := hookCalls
					err = manager.updateConfig(context.Background())
					if tc.wantErr != "" {
						require.ErrorContains(t, err, tc.wantErr)
						require.Same(t, previous, manager.conf.GetDynamicConfiguration())
						require.Same(t, previousCRD, manager.lastDynamicConfigCRD)
						require.Equal(t, previousHookCalls, hookCalls)
						return
					}
					require.NoError(t, err)
					got := manager.conf.GetDynamicConfiguration().FragMemConfiguration
					require.Equal(t, tc.want, got.VMCompactionProactiveness)
					require.Equal(t, tc.enable && tc.remove == "", got.EnableFragMem)
					require.Equal(t, startup, manager.defaultConfig.FragMemConfiguration.VMCompactionProactiveness)
					if tc.want != nil {
						require.NotSame(t, tc.want, got.VMCompactionProactiveness)
					}
				})
			}
		})
	}
}

func Test_getGVRToKindMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wantGVR schema.GroupVersionResource
		wantGVK schema.GroupVersionKind
	}{
		{
			name:    "aqc",
			wantGVR: schema.GroupVersionResource(crd.AdminQoSConfigurationGVR),
			wantGVK: schema.GroupVersionKind{
				Group:   v1alpha1.SchemeGroupVersion.Group,
				Version: v1alpha1.SchemeGroupVersion.Version,
				Kind:    crd.ResourceKindAdminQoSConfiguration,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := getGVRToGVKMap(); !checkGVRToGVKMap(tt.wantGVR, tt.wantGVK, got) {
				t.Errorf("getGVRToGVKMap() = %v, wantGVR %v wantGVK %v", got, tt.wantGVR, tt.wantGVK)
			}
		})
	}
}

func checkGVRToGVKMap(gvr schema.GroupVersionResource, wantGVK schema.GroupVersionKind,
	m map[schema.GroupVersionResource]schema.GroupVersionKind,
) bool {
	gvk, ok := m[gvr]
	if ok && gvk == wantGVK {
		return true
	}
	return false
}

func Test_updateDynamicConf(t *testing.T) {
	t.Parallel()

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
				loader: generateTestLoader(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-1",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
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
				AdminQoSConfiguration: &v1alpha1.AdminQoSConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-1",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
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
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ec := generateTestEvictionConfiguration(map[v1.ResourceName]float64{
				v1.ResourceCPU:    1.2,
				v1.ResourceMemory: 1.3,
			})
			manager := constructTestDynamicConfigManager(t, "node-name", "Test_updateDynamicConf", ec)
			defer os.RemoveAll(manager.conf.CheckpointManagerDir)
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
		v1alpha1.ResourceNameAdminQoSConfigurations: crd.AdminQoSConfigurationGVR,
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
