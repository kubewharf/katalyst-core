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

package plugin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/config"
	tmoconf "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/tmo"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	podfetcher "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func TestTMOEffectiveQoSLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		declaredQoSLevel   string
		nativeQoSClass     string
		expectedLevel      string
		expectedApplicable bool
	}{
		{
			name:               "shared cores with burstable reuses shared cores",
			declaredQoSLevel:   katalystapiconsts.PodAnnotationQoSLevelSharedCores,
			nativeQoSClass:     "Burstable",
			expectedLevel:      katalystapiconsts.PodAnnotationQoSLevelSharedCores,
			expectedApplicable: true,
		},
		{
			name:               "shared cores with guaranteed is treated as dedicated cores",
			declaredQoSLevel:   katalystapiconsts.PodAnnotationQoSLevelSharedCores,
			nativeQoSClass:     "Guaranteed",
			expectedLevel:      katalystapiconsts.PodAnnotationQoSLevelDedicatedCores,
			expectedApplicable: true,
		},
		{
			name:               "shared cores with best effort reuses shared cores",
			declaredQoSLevel:   katalystapiconsts.PodAnnotationQoSLevelSharedCores,
			nativeQoSClass:     "BestEffort",
			expectedLevel:      katalystapiconsts.PodAnnotationQoSLevelSharedCores,
			expectedApplicable: true,
		},
		{
			name:               "shared cores with unknown native qos is not applicable",
			declaredQoSLevel:   katalystapiconsts.PodAnnotationQoSLevelSharedCores,
			nativeQoSClass:     "Unknown",
			expectedLevel:      "",
			expectedApplicable: false,
		},
		{
			name:               "dedicated cores with burstable stays dedicated cores",
			declaredQoSLevel:   katalystapiconsts.PodAnnotationQoSLevelDedicatedCores,
			nativeQoSClass:     "Burstable",
			expectedLevel:      katalystapiconsts.PodAnnotationQoSLevelDedicatedCores,
			expectedApplicable: true,
		},
		{
			name:               "dedicated cores with guaranteed stays dedicated cores",
			declaredQoSLevel:   katalystapiconsts.PodAnnotationQoSLevelDedicatedCores,
			nativeQoSClass:     "Guaranteed",
			expectedLevel:      katalystapiconsts.PodAnnotationQoSLevelDedicatedCores,
			expectedApplicable: true,
		},
		{
			name:               "dedicated cores with best effort stays dedicated cores",
			declaredQoSLevel:   katalystapiconsts.PodAnnotationQoSLevelDedicatedCores,
			nativeQoSClass:     "BestEffort",
			expectedLevel:      katalystapiconsts.PodAnnotationQoSLevelDedicatedCores,
			expectedApplicable: true,
		},
		{
			name:               "reclaimed cores is passed through",
			declaredQoSLevel:   katalystapiconsts.PodAnnotationQoSLevelReclaimedCores,
			nativeQoSClass:     "BestEffort",
			expectedLevel:      katalystapiconsts.PodAnnotationQoSLevelReclaimedCores,
			expectedApplicable: true,
		},
		{
			name:               "system cores is passed through",
			declaredQoSLevel:   katalystapiconsts.PodAnnotationQoSLevelSystemCores,
			nativeQoSClass:     "Guaranteed",
			expectedLevel:      katalystapiconsts.PodAnnotationQoSLevelSystemCores,
			expectedApplicable: true,
		},
		{
			name:               "unknown level is not applicable",
			declaredQoSLevel:   "unknown_cores",
			nativeQoSClass:     "Burstable",
			expectedLevel:      "",
			expectedApplicable: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			level, applicable := TMOEffectiveQoSLevel(tt.declaredQoSLevel, tt.nativeQoSClass)
			assert.Equal(t, tt.expectedApplicable, applicable)
			assert.Equal(t, tt.expectedLevel, level)
		})
	}
}

// Embedded interfaces keep these fakes limited to methods used by Reconcile.
type tmoTestPodFetcher struct {
	podfetcher.PodFetcher
	pods []*corev1.Pod
}

func (f *tmoTestPodFetcher) GetPodList(_ context.Context, filter func(*corev1.Pod) bool) ([]*corev1.Pod, error) {
	var result []*corev1.Pod
	for _, pod := range f.pods {
		if filter == nil || filter(pod) {
			result = append(result, pod)
		}
	}
	return result, nil
}

func (f *tmoTestPodFetcher) GetPod(_ context.Context, uid string) (*corev1.Pod, error) {
	for _, pod := range f.pods {
		if string(pod.UID) == uid {
			return pod, nil
		}
	}
	return nil, fmt.Errorf("pod %s not found", uid)
}

type tmoTestMetricsFetcher struct {
	metrictypes.MetricsFetcher
}

func (f *tmoTestMetricsFetcher) GetContainerMetric(_, _, _ string) (metric.MetricData, error) {
	return metric.MetricData{}, fmt.Errorf("metrics unavailable in configuration tests")
}

type tmoTestServiceProfilingManager struct {
	spd.ServiceProfilingManager
	detail *configv1alpha1.TMOConfigDetail
	poor   bool
}

func (m *tmoTestServiceProfilingManager) ServiceExtendedIndicator(_ context.Context, _ metav1.ObjectMeta, indicators interface{}) (bool, error) {
	indicators.(*configv1alpha1.TransparentMemoryOffloadingIndicators).ConfigDetail = m.detail
	return false, nil
}

func (m *tmoTestServiceProfilingManager) ServiceBusinessPerformanceLevel(_ context.Context, _ metav1.ObjectMeta) (spd.PerformanceLevel, error) {
	if m.poor {
		return spd.PerformanceLevelPoor, nil
	}
	return spd.PerformanceLevelPerfect, nil
}

func (m *tmoTestServiceProfilingManager) ServiceBaseline(_ context.Context, _ metav1.ObjectMeta) (bool, error) {
	return false, nil
}

func newTMOTestPod(uid, namespace, name, declared string, nativeClass corev1.PodQOSClass) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID: types.UID(uid), Namespace: namespace, Name: name,
			Annotations: map[string]string{katalystapiconsts.PodAnnotationQoSLevelKey: declared},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, QOSClass: nativeClass,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "main"}},
		},
	}
	if nativeClass != corev1.PodQOSBestEffort {
		pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi"),
		}
		if nativeClass == corev1.PodQOSGuaranteed {
			pod.Spec.Containers[0].Resources.Limits = pod.Spec.Containers[0].Resources.Requests.DeepCopy()
		}
	}
	return pod
}

func newTMOTestReconciler(pods ...*corev1.Pod) (*transparentMemoryOffloading, *tmoTestPodFetcher, *tmoTestServiceProfilingManager) {
	fetcher := &tmoTestPodFetcher{pods: pods}
	profiles := &tmoTestServiceProfilingManager{}
	server := &metaserver.MetaServer{
		MetaAgent:               &metaagent.MetaAgent{PodFetcher: fetcher, MetricsFetcher: &tmoTestMetricsFetcher{}},
		ServiceProfilingManager: profiles,
	}
	plugin := NewTransparentMemoryOffloading(config.NewConfiguration(), nil, nil, server, metrics.DummyMetrics{}).(*transparentMemoryOffloading)
	return plugin, fetcher, profiles
}

func tmoTestConfig(defaults *tmoconf.TMODefaultConfigurations, interval time.Duration) *tmoconf.TMOConfigDetail {
	detail := tmoconf.NewTMOConfigDetail(defaults)
	detail.EnableTMO = true
	detail.EnableSwap = true
	detail.Interval = interval
	return detail
}

func TestTMOReconcileConfigPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		declared      string
		nativeClass   corev1.PodQOSClass
		pool          string
		service       bool
		poor          bool
		expectedLevel string
	}{
		{"shared burstable", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSBurstable, "", false, false, katalystapiconsts.PodAnnotationQoSLevelSharedCores},
		{"shared best effort", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSBestEffort, "", false, false, katalystapiconsts.PodAnnotationQoSLevelSharedCores},
		{"shared guaranteed", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSGuaranteed, "", false, false, katalystapiconsts.PodAnnotationQoSLevelDedicatedCores},
		{"dedicated burstable", katalystapiconsts.PodAnnotationQoSLevelDedicatedCores, corev1.PodQOSBurstable, "", false, false, katalystapiconsts.PodAnnotationQoSLevelDedicatedCores},
		{"dedicated best effort", katalystapiconsts.PodAnnotationQoSLevelDedicatedCores, corev1.PodQOSBestEffort, "", false, false, katalystapiconsts.PodAnnotationQoSLevelDedicatedCores},
		{"dedicated guaranteed", katalystapiconsts.PodAnnotationQoSLevelDedicatedCores, corev1.PodQOSGuaranteed, "", false, false, katalystapiconsts.PodAnnotationQoSLevelDedicatedCores},
		{"pool overrides qos", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSBestEffort, commonstate.PoolNameShare, false, false, katalystapiconsts.PodAnnotationQoSLevelSharedCores},
		{"guaranteed uses dedicated pool", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSGuaranteed, commonstate.PoolNameDedicated, false, false, katalystapiconsts.PodAnnotationQoSLevelDedicatedCores},
		{"service overrides pool", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSBestEffort, commonstate.PoolNameShare, true, false, katalystapiconsts.PodAnnotationQoSLevelSharedCores},
		{"reclaim restriction overrides service", katalystapiconsts.PodAnnotationQoSLevelDedicatedCores, corev1.PodQOSGuaranteed, commonstate.PoolNameDedicated, true, true, katalystapiconsts.PodAnnotationQoSLevelDedicatedCores},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Existing offloading tests patch process-wide functions under this mutex.
			transparentMemoryOffloadingTestMutex.Lock()
			defer transparentMemoryOffloadingTestMutex.Unlock()

			pod := newTMOTestPod("uid", "default", "pod", tt.declared, tt.nativeClass)
			plugin, _, profiles := newTMOTestReconciler(pod)
			conf := plugin.conf.GetDynamicConfiguration().TransparentMemoryOffloadingConfiguration
			shared := tmoTestConfig(conf.DefaultConfigurations, time.Minute)
			dedicated := tmoTestConfig(conf.DefaultConfigurations, 2*time.Minute)
			conf.QoSLevelConfigs[katalystapiconsts.QoSLevel(katalystapiconsts.PodAnnotationQoSLevelSharedCores)] = shared
			conf.QoSLevelConfigs[katalystapiconsts.QoSLevel(katalystapiconsts.PodAnnotationQoSLevelDedicatedCores)] = dedicated
			expected := shared
			if tt.expectedLevel == katalystapiconsts.PodAnnotationQoSLevelDedicatedCores {
				expected = dedicated
				// A share pool entry must not override a Guaranteed pod's dedicated tier.
				conf.PoolNameConfigs[commonstate.PoolNameShare] = tmoTestConfig(conf.DefaultConfigurations, 9*time.Minute)
			}
			qosInterval := expected.Interval
			if tt.pool != "" {
				expected = tmoTestConfig(conf.DefaultConfigurations, 3*time.Minute)
				conf.PoolNameConfigs[tt.pool] = expected
			}
			if tt.service {
				interval := metav1.Duration{Duration: 4 * time.Minute}
				enabled := true
				profiles.detail = &configv1alpha1.TMOConfigDetail{Interval: &interval, EnableTMO: &enabled, EnableSwap: &enabled}
			}
			profiles.poor = tt.poor
			require.NoError(t, plugin.Reconcile(nil))
			actual := plugin.containerTmoEngines[native.GeneratePodContainerName("uid", "main")].GetConf()
			assert.Equal(t, !tt.poor && tt.expectedLevel != "", actual.EnableTMO)
			assert.Equal(t, !tt.poor && tt.expectedLevel != "", actual.EnableSwap)
			expectedInterval := expected.Interval
			if tt.service {
				expectedInterval = 4 * time.Minute
			}
			assert.Equal(t, expectedInterval, actual.Interval)
			if tt.expectedLevel != "" {
				assert.Equal(t, qosInterval, conf.QoSLevelConfigs[katalystapiconsts.QoSLevel(tt.expectedLevel)].Interval, "engine overrides must not mutate qos configuration")
			}
		})
	}
}

func TestTMOReconcileRemovedConfigReturnsToDefaults(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"qos", "pool", "service"} {
		source := source
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			// Existing offloading tests patch process-wide functions under this mutex.
			transparentMemoryOffloadingTestMutex.Lock()
			defer transparentMemoryOffloadingTestMutex.Unlock()

			pod := newTMOTestPod("uid", "default", "pod", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSBurstable)
			plugin, _, profiles := newTMOTestReconciler(pod)
			conf := plugin.conf.GetDynamicConfiguration().TransparentMemoryOffloadingConfiguration
			detail := tmoTestConfig(conf.DefaultConfigurations, time.Minute)
			detail.ReservedInactiveFile = 1024
			detail.PSIPolicyConf.MaxProbe = 0.5
			detail.RefaultPolicyConf.ReclaimAccuracyTarget = 0.8
			switch source {
			case "qos":
				conf.QoSLevelConfigs[katalystapiconsts.QoSLevel(katalystapiconsts.PodAnnotationQoSLevelSharedCores)] = detail
			case "pool":
				conf.PoolNameConfigs[commonstate.PoolNameShare] = detail
			case "service":
				interval := metav1.Duration{Duration: time.Minute}
				enabled := true
				profiles.detail = &configv1alpha1.TMOConfigDetail{EnableTMO: &enabled, EnableSwap: &enabled, Interval: &interval}
			}
			require.NoError(t, plugin.Reconcile(nil))
			key := native.GeneratePodContainerName("uid", "main")
			engine := plugin.containerTmoEngines[key]
			require.True(t, engine.GetConf().EnableTMO)
			delete(conf.QoSLevelConfigs, katalystapiconsts.QoSLevel(katalystapiconsts.PodAnnotationQoSLevelSharedCores))
			delete(conf.PoolNameConfigs, commonstate.PoolNameShare)
			profiles.detail = nil
			require.NoError(t, plugin.Reconcile(nil))
			assert.Same(t, engine, plugin.containerTmoEngines[key])
			assert.Equal(t, tmoconf.NewTMOConfigDetail(conf.DefaultConfigurations), engine.GetConf())
			assert.Zero(t, engine.GetOffloadingTargetSize())
		})
	}
}

func TestTMOReconcileBlockOverridesPoolAndService(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()

	pod := newTMOTestPod("uid", "default", "pod", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSBestEffort)
	pod.Labels = map[string]string{"psm": "blocked-service"}
	plugin, _, profiles := newTMOTestReconciler(pod)
	conf := plugin.conf.GetDynamicConfiguration().TransparentMemoryOffloadingConfiguration
	conf.PoolNameConfigs[commonstate.PoolNameShare] = tmoTestConfig(conf.DefaultConfigurations, time.Minute)
	conf.BlockConfig.LabelsSelector = labels.SelectorFromSet(labels.Set{"psm": "blocked-service"})
	enabled := true
	profiles.detail = &configv1alpha1.TMOConfigDetail{EnableTMO: &enabled, EnableSwap: &enabled}
	require.NoError(t, plugin.Reconcile(nil))
	actual := plugin.containerTmoEngines[native.GeneratePodContainerName("uid", "main")].GetConf()
	assert.False(t, actual.EnableTMO)
	assert.False(t, actual.EnableSwap)
}

func TestTMOReconcileEngineIdentityAndMetadata(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()

	pod := newTMOTestPod("uid-a", "ns-a", "same-name", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSBurstable)
	other := newTMOTestPod("uid-b", "ns-b", "same-name", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSGuaranteed)
	plugin, fetcher, _ := newTMOTestReconciler(pod, other)
	require.NoError(t, plugin.Reconcile(nil))
	require.Len(t, plugin.containerTmoEngines, 2)
	key := native.GeneratePodContainerName("uid-a", "main")
	engine := plugin.containerTmoEngines[key].(*tmoEngineInstance)
	sampledAt := time.Now()
	engine.lastTime = sampledAt
	engine.lastStats.offloadingTargetSize = 4096

	updated := pod.DeepCopy()
	updated.Labels = map[string]string{"psm": "new-service"}
	fetcher.pods = []*corev1.Pod{updated, other}
	require.NoError(t, plugin.Reconcile(nil))
	assert.Same(t, engine, plugin.containerTmoEngines[key])
	assert.Equal(t, "ns-a", engine.GetContainerInfo().PodNamespace)
	assert.Equal(t, updated.Labels, engine.GetContainerInfo().Labels)
	assert.Equal(t, sampledAt, engine.lastTime)

	// A native QoS change with the same UID must reset the sampling history.
	changed := newTMOTestPod("uid-a", "ns-a", "same-name", katalystapiconsts.PodAnnotationQoSLevelSharedCores, corev1.PodQOSGuaranteed)
	fetcher.pods = []*corev1.Pod{changed, other}
	require.NoError(t, plugin.Reconcile(nil))
	replacement := plugin.containerTmoEngines[key].(*tmoEngineInstance)
	assert.NotSame(t, engine, replacement)
	assert.True(t, replacement.lastTime.IsZero())
	assert.Equal(t, TmoStats{}, replacement.lastStats)

	// Declared QoS changes also require fresh sampling state.
	replacement.lastTime = sampledAt
	changed.Annotations[katalystapiconsts.PodAnnotationQoSLevelKey] = katalystapiconsts.PodAnnotationQoSLevelDedicatedCores
	require.NoError(t, plugin.Reconcile(nil))
	declaredReplacement := plugin.containerTmoEngines[key].(*tmoEngineInstance)
	assert.NotSame(t, replacement, declaredReplacement)
	assert.True(t, declaredReplacement.lastTime.IsZero())
	assert.Equal(t, changed.Annotations, declaredReplacement.GetContainerInfo().Annotations)

	// Recreated pods with the same name get a new engine, and the old UID is pruned.
	changed.UID = "uid-c"
	fetcher.pods = []*corev1.Pod{changed, other}
	require.NoError(t, plugin.Reconcile(nil))
	assert.NotContains(t, plugin.containerTmoEngines, key)
	newKey := native.GeneratePodContainerName("uid-c", "main")
	require.Contains(t, plugin.containerTmoEngines, newKey)
	assert.Equal(t, "uid-c", plugin.containerTmoEngines[newKey].GetContainerInfo().PodUID)
	advices := plugin.GetAdvices()
	require.Len(t, advices.ContainerEntries, 2)
	for _, advice := range advices.ContainerEntries {
		assert.NotEqual(t, "uid-a", advice.PodUID)
	}
}
