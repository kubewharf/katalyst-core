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

package fetcher

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/kubelet/config"
	"k8s.io/kubernetes/pkg/kubelet/pluginmanager"
	plugincache "k8s.io/kubernetes/pkg/kubelet/pluginmanager/cache"

	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/reporter"
	katalystconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	reporterconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/reporter"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	testPluginName       = "fake-reporter-plugin-1"
	testPluginNameSecond = "fake-reporter-plugin-2"
	testPluginNameThird  = "fake-reporter-plugin-3"
)

var (
	testGroupVersionKind = v1.GroupVersionKind{
		Group:   "test-group",
		Kind:    "test-kind",
		Version: "test-version",
	}
)

func tmpSocketDir() (socketDir string, err error) {
	socketDir, err = ioutil.TempDir("", "reporter_plugin")
	if err != nil {
		return
	}
	_ = os.MkdirAll(socketDir, 0755)
	return
}

func generateTestConfiguration(dir string) *katalystconfig.Configuration {
	return &katalystconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			GenericAgentConfiguration: &agent.GenericAgentConfiguration{
				MetaServerConfiguration: &metaserver.MetaServerConfiguration{CheckpointManagerDir: dir},
				GenericReporterConfiguration: &reporterconfig.GenericReporterConfiguration{
					CollectInterval: 5 * time.Second,
				},
			},
		},
	}
}

func TestNewManagerImpl(t *testing.T) {
	t.Parallel()

	socketDir, err := tmpSocketDir()
	testReporter := reporter.NewReporterManagerStub()
	require.NoError(t, err)
	defer os.RemoveAll(socketDir)
	_, err = NewReporterPluginManager(testReporter, metrics.DummyMetrics{}, nil, generateTestConfiguration(socketDir))
	require.NoError(t, err)
	os.RemoveAll(socketDir)
}

// Tests that the device plugin manager correctly handles registration and re-registration by
// making sure that after registration, devices are correctly updated and if a re-registration
// happens, we will NOT delete devices; and no orphaned devices left.
func TestReporterPluginReRegistration(t *testing.T) {
	t.Parallel()

	socketDir, err := tmpSocketDir()
	require.NoError(t, err)
	defer os.RemoveAll(socketDir)

	content1 := []*v1alpha1.ReportContent{
		{
			GroupVersionKind: &testGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: "fieldName_a",
					Value:     []byte("Value_a"),
				},
			},
		},
	}

	testReporter := reporter.NewReporterManagerStub()

	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()
	_, ch, p1 := setup(t, ctx, content1, nil, socketDir, testPluginName, testReporter)

	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout while waiting for manager update")
	}

	p1GetReportContentResponse := testReporter.GetReportContentResponse(p1.Name())
	require.NotNil(t, p1GetReportContentResponse)
	reporterContentsEqual(t, content1, p1GetReportContentResponse.Content)

	content2 := []*v1alpha1.ReportContent{
		{
			GroupVersionKind: &testGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: "fieldName_b",
					Value:     []byte("Value_b"),
				},
			},
		},
	}

	p2 := setupReporterPlugin(t, content2, socketDir, testPluginNameSecond)

	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout while waiting for manager update")
	}

	p1GetReportContentResponse = testReporter.GetReportContentResponse(p1.Name())
	require.NotNil(t, p1GetReportContentResponse)
	reporterContentsEqual(t, content1, p1GetReportContentResponse.Content)

	p2GetReportContentResponse := testReporter.GetReportContentResponse(p2.Name())
	require.NotNil(t, p2GetReportContentResponse)
	reporterContentsEqual(t, content2, p2GetReportContentResponse.Content)

	// test the scenario that plugin de-register and graceful shut down
	_ = p1.Stop()
	_ = p2.Stop()
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	socketDir, err := tmpSocketDir()
	require.NoError(t, err)
	defer os.RemoveAll(socketDir)

	content1 := []*v1alpha1.ReportContent{
		{
			GroupVersionKind: &testGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: "fieldName_a",
					Value:     []byte("Value_a"),
				},
			},
		},
	}

	testReporter := reporter.NewReporterManagerStub()

	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()
	_, ch, p := setup(t, ctx, content1, nil, socketDir, testPluginNameThird, testReporter)

	select {
	case <-ch:
	case <-time.After(6 * time.Second):
		t.Fatalf("timeout while waiting for manager update")
	}

	results := general.CheckHealthz()
	for name, response := range results {
		if reporterFetcherRules.Has(string(name)) {
			require.Equal(t, response.State, general.HealthzCheckStateReady)
		}
	}

	_ = p.Stop()
}

func setup(t *testing.T, ctx context.Context, content []*v1alpha1.ReportContent, callback plugin.ListAndWatchCallback, socketDir string, pluginSocketName string, reporter reporter.Manager) (registration.AgentPluginHandler, <-chan interface{}, skeleton.GenericPlugin) {
	m, updateChan := setupReporterManager(t, ctx, content, socketDir, callback, reporter)
	p := setupReporterPlugin(t, content, socketDir, pluginSocketName)
	return m, updateChan, p
}

func setupReporterManager(t *testing.T, ctx context.Context, content []*v1alpha1.ReportContent, socketDir string, callback plugin.ListAndWatchCallback, reporter reporter.Manager) (registration.AgentPluginHandler, <-chan interface{}) {
	m, err := NewReporterPluginManager(reporter, metrics.DummyMetrics{}, nil, generateTestConfiguration(socketDir))
	require.NoError(t, err)
	updateChan := make(chan interface{})

	if callback != nil {
		m.callback = callback
	}

	originalCallback := m.callback
	m.callback = func(pluginName string, response *v1alpha1.GetReportContentResponse) {
		originalCallback(pluginName, response)
		updateChan <- new(interface{})
	}

	go m.Run(ctx)

	pluginManager := pluginmanager.NewPluginManager(
		socketDir,
		&record.FakeRecorder{},
	)

	pluginManager.AddHandler(m.GetHandlerType(), plugincache.PluginHandler(m))

	go pluginManager.Run(config.NewSourcesReady(func(_ sets.String) bool { return true }), ctx.Done())

	return m, updateChan
}

func setupReporterPlugin(t *testing.T, content []*v1alpha1.ReportContent, socketDir string, pluginName string) skeleton.GenericPlugin {
	p, _ := skeleton.NewRegistrationPluginWrapper(
		skeleton.NewReporterPluginStub(content, pluginName),
		[]string{socketDir}, nil)
	err := p.Start()
	require.NoError(t, err)
	return p
}

func reporterContentsEqual(t *testing.T, expected, actual []*v1alpha1.ReportContent) {
	require.Equal(t, len(expected), len(actual))
	for idx := range expected {
		require.Equal(t, expected[idx].GroupVersionKind, actual[idx].GroupVersionKind)
		require.Equal(t, len(expected[idx].Field), len(actual[idx].Field))
		for fieldIdx := range expected[idx].Field {
			require.Equal(t, expected[idx].Field[fieldIdx].FieldType, actual[idx].Field[fieldIdx].FieldType)
			require.Equal(t, expected[idx].Field[fieldIdx].FieldName, actual[idx].Field[fieldIdx].FieldName)
			require.Equal(t, expected[idx].Field[fieldIdx].Value, actual[idx].Field[fieldIdx].Value)
		}
	}
}
