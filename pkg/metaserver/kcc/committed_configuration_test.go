/*
Copyright 2026 The Katalyst Authors.

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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

// This loader preserves each registered CRD's concrete type without an AQC assumption.
type committedConfigurationCRDLoader struct {
	source *crd.DynamicConfigCRD
}

func (l committedConfigurationCRDLoader) LoadConfig(_ context.Context, _ metav1.GroupVersionResource, conf interface{}) error {
	fields := reflect.ValueOf(deepCopyDynamicConfigCRD(l.source)).Elem()
	target := reflect.ValueOf(conf)
	for i := 0; i < fields.NumField(); i++ {
		field := fields.Field(i)
		if field.Type() == target.Type() && !field.IsNil() {
			target.Elem().Set(field.Elem())
			return nil
		}
	}
	return fmt.Errorf("missing test configuration %T", conf)
}

func testCommittedConfigurationAuditsAllSources(t *testing.T) {
	logState := klog.CaptureState()
	defer logState.Restore()
	klog.LogToStderr(false)
	for _, withAQC := range []bool{false, true} {
		for _, metadataOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("aqc=%t/metadata_only=%t", withAQC, metadataOnly), func(t *testing.T) {
				source := &crd.DynamicConfigCRD{
					AuthConfiguration:                        &v1alpha1.AuthConfiguration{},
					TransparentMemoryOffloadingConfiguration: &v1alpha1.TransparentMemoryOffloadingConfiguration{},
					StrategyGroup:                            &v1alpha1.StrategyGroup{},
					IRQTuningConfiguration:                   &v1alpha1.IRQTuningConfiguration{},
				}
				if withAQC {
					source.AdminQoSConfiguration = generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.2})
				}
				manager := constructTestDynamicConfigManager(t, "test-node", t.TempDir(), generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.2}))
				manager.resourceGVRMap = make(map[string]metav1.GroupVersionResource)
				fields := reflect.ValueOf(source).Elem()
				for i := 0; i < fields.NumField(); i++ {
					if fields.Field(i).IsNil() {
						continue
					}
					object := fields.Field(i).Interface().(metav1.Object)
					object.SetName("configuration")
					object.SetNamespace("test-namespace")
					object.SetResourceVersion("2")
					kind := fields.Type().Field(i).Name
					for gvr, gvk := range katalystConfigGVRToGVKMap {
						if gvk.Kind == kind {
							manager.resourceGVRMap[kind] = metav1.GroupVersionResource(gvr)
							break
						}
					}
				}
				manager.configLoader = committedConfigurationCRDLoader{source: source}
				source.AuthConfiguration.Spec.Config.BasicAuthConfig = &v1alpha1.BasicAuthConfig{
					UserPasswordPairs: []v1alpha1.UserPasswordPair{{Username: "test-user", Password: "auth-sentinel"}},
				}
				enabled := true
				source.IRQTuningConfiguration.Spec.Config.EnableTuner = &enabled
				source.StrategyGroup.Status.EnabledStrategies = []v1alpha1.Strategy{{Parameters: map[string]string{"mode": "enabled"}}}
				manager.defaultConfig.TransparentMemoryOffloadingConfiguration.PoolNameConfigs["shared"] = nil
				hookCalls := 0
				manager.AddConfigHook(func(_ context.Context, _, next *dynamic.Configuration) error {
					hookCalls++
					next.TuningInterval = 17
					return nil
				})
				if metadataOnly {
					previous := deepCopyDynamicConfigCRD(source)
					previousFields := reflect.ValueOf(previous).Elem()
					for i := 0; i < previousFields.NumField(); i++ {
						if !previousFields.Field(i).IsNil() {
							previousFields.Field(i).Interface().(metav1.Object).SetResourceVersion("1")
						}
					}
					conf := deepCopy(manager.defaultConfig)
					applyDynamicConfig(conf, previous)
					manager.conf.SetDynamicConfiguration(conf)
					manager.lastDynamicConfigCRD = previous
				}
				var output bytes.Buffer
				klog.SetOutput(&output)
				require.NoError(t, manager.updateConfig(context.Background()))
				klog.Flush()
				records := strings.Split(output.String(), "dynamic configuration committed: ")
				require.Len(t, records, 2, "non-AQC publication must have a committed record")
				var record struct {
					Sources map[string]map[string]interface{} `json:"sources"`
					Values  map[string]json.RawMessage        `json:"values"`
				}
				require.NoError(t, json.Unmarshal([]byte(strings.Split(records[1], "\n")[0]), &record))
				count := 4
				if withAQC {
					count++
				}
				require.Len(t, record.Sources, count)
				for i := 0; i < fields.NumField(); i++ {
					if !fields.Field(i).IsNil() {
						kind := fields.Type().Field(i).Name
						require.Contains(t, record.Sources, kind)
						if kind != crd.ResourceKindAuthConfiguration {
							spec, err := json.Marshal(fields.Field(i).Elem().FieldByName("Spec").Interface())
							require.NoError(t, err)
							require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(spec)), record.Sources[kind]["spec_sha256"])
						}
					}
				}
				for kind, metadata := range record.Sources {
					require.Equal(t, "2", metadata["resource_version"], kind)
					require.Equal(t, "configuration", metadata["name"], kind)
					if kind == crd.ResourceKindAuthConfiguration {
						require.NotContains(t, metadata, "spec_sha256", "do not fingerprint credential payloads")
					}
				}
				published := manager.conf.GetDynamicConfiguration()
				require.True(t, published.EnableTuner)
				require.Equal(t, "enabled", published.EnabledStrategies[0].Parameters["mode"])
				if metadataOnly {
					require.Zero(t, hookCalls)
				} else {
					require.Equal(t, 1, hookCalls)
					require.Equal(t, 17, published.TuningInterval)
				}
				for kind, value := range map[string]interface{}{
					crd.ResourceKindAdminQoSConfiguration:                    published.AdminQoSConfiguration,
					crd.ResourceKindTransparentMemoryOffloadingConfiguration: published.TransparentMemoryOffloadingConfiguration,
					crd.ResourceKindStrategyGroup:                            published.StrategyGroupConfiguration,
					crd.ResourceKindIRQTuningConfiguration:                   published.IRQTuningConfiguration,
				} {
					if kind == crd.ResourceKindAdminQoSConfiguration && !withAQC {
						require.NotContains(t, record.Values, kind)
						continue
					}
					expected, err := json.Marshal(value)
					require.NoError(t, err)
					require.JSONEq(t, string(expected), string(record.Values[kind]), kind)
				}
				require.Len(t, record.Values, count-1, "only reviewed domains with an adopted source may be logged")
				require.NotContains(t, record.Values, crd.ResourceKindAuthConfiguration)
				require.NotContains(t, records[1], "auth-sentinel")
				require.NotContains(t, records[1], "UserPasswordPairs")
			})
		}
	}
}

func testCommittedConfigurationAuthOnlyHasMetadataWithoutCredentials(t *testing.T) {
	logState := klog.CaptureState()
	defer logState.Restore()
	klog.LogToStderr(false)
	var output bytes.Buffer
	klog.SetOutput(&output)
	manager := constructTestDynamicConfigManager(t, "test-node", t.TempDir(), generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.2}))
	source := &crd.DynamicConfigCRD{AuthConfiguration: &v1alpha1.AuthConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", ResourceVersion: "2"},
	}}
	source.AuthConfiguration.Spec.Config.BasicAuthConfig = &v1alpha1.BasicAuthConfig{
		UserPasswordPairs: []v1alpha1.UserPasswordPair{{Username: "test-user", Password: "auth-only-sentinel"}},
	}
	manager.resourceGVRMap = map[string]metav1.GroupVersionResource{"authconfigurations": crd.AuthConfigurationGVR}
	manager.configLoader = committedConfigurationCRDLoader{source: source}
	require.NoError(t, manager.updateConfig(context.Background()))
	require.Equal(t, "auth-only-sentinel", manager.conf.GetDynamicConfiguration().UserPasswordPairs[0].Password)
	require.NoError(t, manager.updateConfig(context.Background())) // Unchanged source does not create another acknowledgement.
	klog.Flush()
	records := strings.Split(output.String(), "dynamic configuration committed: ")
	require.Len(t, records, 2)
	var record struct {
		Sources map[string]map[string]interface{} `json:"sources"`
		Values  map[string]json.RawMessage        `json:"values"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.Split(records[1], "\n")[0]), &record))
	require.Len(t, record.Sources, 1)
	require.Equal(t, "2", record.Sources[crd.ResourceKindAuthConfiguration]["resource_version"])
	require.NotContains(t, record.Sources[crd.ResourceKindAuthConfiguration], "spec_sha256")
	require.Empty(t, record.Values, "auth-only updates must not dump unrelated configuration values")
	require.NotContains(t, output.String(), "auth-only-sentinel")
}

func testCommittedConfigurationAuditUsesPublishedValues(t *testing.T) {
	// klog output is process-global; capture only in this serial test and restore it.
	logState := klog.CaptureState()
	defer logState.Restore()
	var output bytes.Buffer
	klog.LogToStderr(false)
	klog.SetOutput(&output)
	manager := constructTestDynamicConfigManager(t, "test-node", t.TempDir(),
		generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.2}))
	before := manager.conf.GetDynamicConfiguration()
	before.UserPasswordPairs = []v1alpha1.UserPasswordPair{{Username: "test-user", Password: "test-auth-sentinel"}}
	baseline := deepCopy(before)
	manager.AddConfigHook(func(_ context.Context, _, next *dynamic.Configuration) error {
		next.EnableReclaim = !before.EnableReclaim
		return fmt.Errorf("reject candidate")
	})
	require.Error(t, manager.updateConfig(context.Background()))
	klog.Flush()
	require.NotContains(t, output.String(), "dynamic configuration committed:")
	require.Same(t, before, manager.conf.GetDynamicConfiguration())
	require.Equal(t, baseline, manager.conf.GetDynamicConfiguration())
	manager.configHooks = nil
	manager.AddConfigHook(func(_ context.Context, _, next *dynamic.Configuration) error {
		next.EnableReclaim = true
		next.EnableBypassCPUSetAdjustment = true
		next.BulkheadConfig.EnableBulkheadCpusetTopology = true
		next.BulkheadConfig.NonReclaimPoolMinSize = 17
		next.EvictionThreshold[v1.ResourceCPU] = 2.5
		next.UserPasswordPairs = []v1alpha1.UserPasswordPair{{Username: "test-user", Password: "test-auth-sentinel"}}
		return nil
	})
	require.NoError(t, manager.updateConfig(context.Background()))
	klog.Flush()
	recordText := strings.Split(output.String(), "dynamic configuration committed: ")
	require.Len(t, recordText, 2)
	var record struct {
		Sources map[string]struct {
			SpecSHA256 string `json:"spec_sha256"`
		} `json:"sources"`
		Values map[string]json.RawMessage `json:"values"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.Split(recordText[1], "\n")[0]), &record))
	spec, err := json.Marshal(manager.lastDynamicConfigCRD.AdminQoSConfiguration.Spec)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(spec)), record.Sources[crd.ResourceKindAdminQoSConfiguration].SpecSHA256)
	expected, err := json.Marshal(manager.conf.GetDynamicConfiguration().AdminQoSConfiguration)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(record.Values[crd.ResourceKindAdminQoSConfiguration]))
	var values struct {
		EnableReclaim                bool
		EnableBypassCPUSetAdjustment bool
		BulkheadConfig               struct {
			EnableBulkheadCpusetTopology bool
			NonReclaimPoolMinSize        int64
		}
		EvictionThreshold map[v1.ResourceName]float64
	}
	require.NoError(t, json.Unmarshal(record.Values[crd.ResourceKindAdminQoSConfiguration], &values))
	require.True(t, values.EnableReclaim)
	require.True(t, values.EnableBypassCPUSetAdjustment)
	require.True(t, values.BulkheadConfig.EnableBulkheadCpusetTopology)
	require.Equal(t, int64(17), values.BulkheadConfig.NonReclaimPoolMinSize)
	require.Equal(t, 2.5, values.EvictionThreshold[v1.ResourceCPU])
	require.NotContains(t, string(record.Values[crd.ResourceKindAdminQoSConfiguration]), "UserPasswordPairs")
	require.NotContains(t, string(record.Values[crd.ResourceKindAdminQoSConfiguration]), crd.ResourceKindAuthConfiguration)
	require.NotContains(t, string(record.Values[crd.ResourceKindAdminQoSConfiguration]), "test-auth-sentinel")
	require.Contains(t, string(record.Values[crd.ResourceKindAdminQoSConfiguration]), `"DisableSharedCoresRampUp":false`)
}

// Observe at the actual log sink: publication must be complete, mux available,
// and the next update excluded until the acknowledgement has been emitted.
type committedConfigurationLockObserver struct {
	manager                     *DynamicConfigManager
	seen, muxAvailable, ordered bool
}

func (o *committedConfigurationLockObserver) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("dynamic configuration committed:")) {
		o.seen = true
		o.muxAvailable = o.manager.mux.TryLock()
		if o.muxAvailable {
			o.manager.mux.Unlock()
		}
		o.ordered = !o.manager.updateMu.TryLock()
		if !o.ordered {
			o.manager.updateMu.Unlock()
		}
	}
	return len(p), nil
}

func testCommittedConfigurationLogReleasesPublishLock(t *testing.T) {
	logState := klog.CaptureState()
	defer logState.Restore()
	klog.LogToStderr(false)
	for _, metadataOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("metadata_only=%t", metadataOnly), func(t *testing.T) {
			aqc := generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.2})
			aqc.ResourceVersion = "2"
			manager := constructTestDynamicConfigManager(t, "test-node", t.TempDir(), aqc)
			if metadataOnly {
				old := aqc.DeepCopy()
				old.ResourceVersion = "1"
				previous := &crd.DynamicConfigCRD{AdminQoSConfiguration: old}
				conf := deepCopy(manager.defaultConfig)
				applyDynamicConfig(conf, previous)
				manager.conf.SetDynamicConfiguration(conf)
				manager.lastDynamicConfigCRD = previous
			}
			observer := &committedConfigurationLockObserver{manager: manager}
			klog.SetOutput(observer)
			require.NoError(t, manager.updateConfig(context.Background()))
			require.True(t, observer.seen)
			require.True(t, observer.muxAvailable, "encoding and sink must not hold publish lock")
			require.True(t, observer.ordered, "next publication cannot overtake its commit log")
			require.Equal(t, "2", manager.lastDynamicConfigCRD.AdminQoSConfiguration.ResourceVersion)
		})
	}
}

// Block only a commit record so the test exercises the real deferred log boundary.
type committedConfigurationBlockingSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	output  bytes.Buffer
}

func (s *committedConfigurationBlockingSink) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("dynamic configuration committed:")) {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	return s.output.Write(p)
}

type committedConfigurationSequenceLoader struct {
	configs []*v1alpha1.AdminQoSConfiguration
	loaded  chan int
	calls   int
}

func (l *committedConfigurationSequenceLoader) LoadConfig(_ context.Context, _ metav1.GroupVersionResource, conf interface{}) error {
	*conf.(*v1alpha1.AdminQoSConfiguration) = *l.configs[l.calls].DeepCopy()
	l.calls++
	l.loaded <- l.calls
	return nil
}

func testCommittedConfigurationSlowLogPreservesPublicationOrder(t *testing.T) {
	logState := klog.CaptureState()
	defer logState.Restore()
	klog.LogToStderr(false)
	for _, metadataOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("metadata_only=%t", metadataOnly), func(t *testing.T) {
			first := generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.2})
			first.ResourceVersion = "2"
			second := generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.3})
			second.ResourceVersion = "3"
			manager := constructTestDynamicConfigManager(t, "test-node", t.TempDir(), first)
			if metadataOnly {
				previous := &crd.DynamicConfigCRD{AdminQoSConfiguration: first.DeepCopy()}
				previous.AdminQoSConfiguration.ResourceVersion = "1"
				conf := deepCopy(manager.defaultConfig)
				applyDynamicConfig(conf, previous)
				manager.conf.SetDynamicConfiguration(conf)
				manager.lastDynamicConfigCRD = previous
			}
			loader := &committedConfigurationSequenceLoader{configs: []*v1alpha1.AdminQoSConfiguration{first, second}, loaded: make(chan int, 2)}
			manager.configLoader = loader
			sink := &committedConfigurationBlockingSink{entered: make(chan struct{}), release: make(chan struct{})}
			klog.SetOutput(sink)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(sink.release) }) }
			defer release()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- manager.updateConfig(ctx) }()
			select {
			case <-sink.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("commit did not reach sink")
			}
			require.Equal(t, 1, <-loader.loaded)
			watcherDone := make(chan error, 1)
			go func() { watcherDone <- manager.AddConfigWatcher(testTargetGVR) }()
			select {
			case err := <-watcherDone:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("watcher blocked by commit log")
			}
			manager.mux.RLock()
			published := deepCopyDynamicConfigCRD(manager.lastDynamicConfigCRD)
			manager.mux.RUnlock()
			require.Equal(t, "2", published.AdminQoSConfiguration.ResourceVersion)
			cancel() // Cancellation after publication does not retract the commit.
			secondDone := make(chan error, 1)
			started := make(chan struct{})
			go func() { close(started); secondDone <- manager.updateConfig(context.Background()) }()
			<-started
			select {
			case <-loader.loaded:
				t.Fatal("next update loaded before current commit log finished")
			case <-time.After(20 * time.Millisecond):
			}
			release()
			require.NoError(t, <-done)
			require.NoError(t, <-secondDone)
			require.Equal(t, 2, <-loader.loaded)
			require.Equal(t, "3", manager.lastDynamicConfigCRD.AdminQoSConfiguration.ResourceVersion)
			records := strings.Split(sink.output.String(), "dynamic configuration committed: ")
			require.Len(t, records, 3)
			for i, source := range []*v1alpha1.AdminQoSConfiguration{first, second} {
				var record struct {
					Sources map[string]struct {
						ResourceVersion string `json:"resource_version"`
						SpecSHA256      string `json:"spec_sha256"`
					} `json:"sources"`
					Values map[string]json.RawMessage `json:"values"`
				}
				require.NoError(t, json.Unmarshal([]byte(strings.Split(records[i+1], "\n")[0]), &record))
				spec, err := json.Marshal(source.Spec)
				require.NoError(t, err)
				require.Equal(t, source.ResourceVersion, record.Sources[crd.ResourceKindAdminQoSConfiguration].ResourceVersion)
				require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(spec)), record.Sources[crd.ResourceKindAdminQoSConfiguration].SpecSHA256)
				var values struct{ EvictionThreshold map[v1.ResourceName]float64 }
				require.NoError(t, json.Unmarshal(record.Values[crd.ResourceKindAdminQoSConfiguration], &values))
				require.Equal(t, source.Spec.Config.EvictionConfig.ReclaimedResourcesEvictionConfig.EvictionThreshold[v1.ResourceCPU], values.EvictionThreshold[v1.ResourceCPU])
			}
		})
	}
}

func testCommittedConfigurationEncodingFailureReleasesLocks(t *testing.T) {
	logState := klog.CaptureState()
	defer logState.Restore()
	klog.LogToStderr(false)
	var output bytes.Buffer
	klog.SetOutput(&output)
	manager := constructTestDynamicConfigManager(t, "test-node", t.TempDir(), generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.2}))
	manager.AddConfigHook(func(_ context.Context, _, next *dynamic.Configuration) error {
		next.InitialRampUpReclaimCPUSetRatio = math.NaN()
		return nil
	})
	require.NoError(t, manager.updateConfig(context.Background()))
	require.NotContains(t, output.String(), "dynamic configuration committed:")
	require.Contains(t, output.String(), "failed to encode committed configuration")
	require.True(t, manager.mux.TryLock())
	manager.mux.Unlock()
	require.True(t, manager.updateMu.TryLock())
	manager.updateMu.Unlock()
}

func testCommittedConfigurationRejectsWatcherDriftWithoutAudit(t *testing.T) {
	logState := klog.CaptureState()
	defer logState.Restore()
	klog.LogToStderr(false)
	var output bytes.Buffer
	klog.SetOutput(&output)
	manager := constructTestDynamicConfigManager(t, "test-node", t.TempDir(), generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.2}))
	before := manager.conf.GetDynamicConfiguration()
	manager.AddConfigHook(func(_ context.Context, _, _ *dynamic.Configuration) error {
		return manager.AddConfigWatcher(metav1.GroupVersionResource{Group: "config.katalyst.kubewharf.io", Version: "v1alpha1", Resource: "authconfigurations"})
	})
	err := manager.updateConfig(context.Background())
	require.ErrorContains(t, err, "config watchers changed")
	require.Same(t, before, manager.conf.GetDynamicConfiguration())
	require.Nil(t, manager.lastDynamicConfigCRD)
	require.NotContains(t, output.String(), "dynamic configuration committed:")
}

func TestCommittedConfigurationLoaderReturnsIsolatedAQC(t *testing.T) {
	t.Parallel()
	manager := constructTestDynamicConfigManager(t, "test-node", t.TempDir(), generateTestEvictionConfiguration(map[v1.ResourceName]float64{v1.ResourceCPU: 1.2}))
	first := &v1alpha1.AdminQoSConfiguration{}
	require.NoError(t, manager.configLoader.LoadConfig(context.Background(), testTargetGVR, first))
	first.ResourceVersion = "mutated"
	first.Spec.Config.EvictionConfig.ReclaimedResourcesEvictionConfig.EvictionThreshold[v1.ResourceCPU] = 99
	second := &v1alpha1.AdminQoSConfiguration{}
	require.NoError(t, manager.configLoader.LoadConfig(context.Background(), testTargetGVR, second))
	require.NotEqual(t, "mutated", second.ResourceVersion)
	require.Equal(t, 1.2, second.Spec.Config.EvictionConfig.ReclaimedResourcesEvictionConfig.EvictionThreshold[v1.ResourceCPU])
}

// klog capture is process-global. Each test owns a helper process selected only
// by an explicit test flag; the parent never changes logging state.
var committedConfigurationHelper = flag.Bool("committed-configuration-helper", false, "run the isolated configuration audit fixture")

func TestCommittedConfigurationAuditsAllSources(t *testing.T) {
	t.Parallel()
	if *committedConfigurationHelper {
		testCommittedConfigurationAuditsAllSources(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommittedConfigurationAuditsAllSources$", "-committed-configuration-helper")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "audit subprocess failed: %s", output)
}

func TestCommittedConfigurationAuthOnlyHasMetadataWithoutCredentials(t *testing.T) {
	t.Parallel()
	if *committedConfigurationHelper {
		testCommittedConfigurationAuthOnlyHasMetadataWithoutCredentials(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommittedConfigurationAuthOnlyHasMetadataWithoutCredentials$", "-committed-configuration-helper")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "audit subprocess failed: %s", output)
}

func TestCommittedConfigurationAuditUsesPublishedValues(t *testing.T) {
	t.Parallel()
	if *committedConfigurationHelper {
		testCommittedConfigurationAuditUsesPublishedValues(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommittedConfigurationAuditUsesPublishedValues$", "-committed-configuration-helper")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "audit subprocess failed: %s", output)
}

func TestCommittedConfigurationLogReleasesPublishLock(t *testing.T) {
	t.Parallel()
	if *committedConfigurationHelper {
		testCommittedConfigurationLogReleasesPublishLock(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommittedConfigurationLogReleasesPublishLock$", "-committed-configuration-helper")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "audit subprocess failed: %s", output)
}

func TestCommittedConfigurationSlowLogPreservesPublicationOrder(t *testing.T) {
	t.Parallel()
	if *committedConfigurationHelper {
		testCommittedConfigurationSlowLogPreservesPublicationOrder(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommittedConfigurationSlowLogPreservesPublicationOrder$", "-committed-configuration-helper")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "audit subprocess failed: %s", output)
}

func TestCommittedConfigurationEncodingFailureReleasesLocks(t *testing.T) {
	t.Parallel()
	if *committedConfigurationHelper {
		testCommittedConfigurationEncodingFailureReleasesLocks(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommittedConfigurationEncodingFailureReleasesLocks$", "-committed-configuration-helper")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "audit subprocess failed: %s", output)
}

func TestCommittedConfigurationRejectsWatcherDriftWithoutAudit(t *testing.T) {
	t.Parallel()
	if *committedConfigurationHelper {
		testCommittedConfigurationRejectsWatcherDriftWithoutAudit(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommittedConfigurationRejectsWatcherDriftWithoutAudit$", "-committed-configuration-helper")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "audit subprocess failed: %s", output)
}
