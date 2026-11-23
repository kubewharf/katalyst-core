//go:build linux
// +build linux

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

package fragmem

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type compactionProactivenessEmitter struct {
	metrics.DummyMetrics
	names  []string
	values []int64
	types  []metrics.MetricTypeName
}

func (e *compactionProactivenessEmitter) StoreInt64(name string, value int64, metricType metrics.MetricTypeName, _ ...metrics.MetricTag) error {
	e.names = append(e.names, name)
	e.values = append(e.values, value)
	e.types = append(e.types, metricType)
	return nil
}

func TestVMCompactionProactivenessDynamicConfiguration(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "compaction_proactiveness")
	require.NoError(t, os.WriteFile(path, []byte("20\n"), 0o600))
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	zero, hundred := int64(0), int64(100)
	emitter := &compactionProactivenessEmitter{}
	for _, tc := range []struct {
		name      string
		target    *int64
		enable    bool
		hostValue string
		want      string
	}{
		{name: "unset observes current", want: "20\n"},
		{name: "zero while FragMem disabled", target: &zero, want: "0\n"},
		{name: "new target while FragMem enabled", target: &hundred, enable: true, want: "100\n"},
		{name: "removing target stops enforcement", want: "100\n"},
		{name: "observe subsequent host change", hostValue: "30\n", want: "30\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hostValue != "" {
				require.NoError(t, os.WriteFile(path, []byte(tc.hostValue), 0o600))
			}
			oldTime := time.Unix(946684800, 0)
			require.NoError(t, os.Chtimes(path, oldTime, oldTime))
			// Replace the snapshot, as KCC does when an AQC update succeeds.
			next := dynamicconfig.NewConfiguration()
			next.FragMemConfiguration.VMCompactionProactiveness = tc.target
			next.FragMemConfiguration.EnableFragMem = tc.enable
			dynamicConf.SetDynamicConfiguration(next)
			require.NoError(t, setVMCompactionProactivenessAtPath(dynamicConf, path, emitter))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(data))
			if tc.target == nil {
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.True(t, oldTime.Equal(info.ModTime()), "an unset target must not write")
			}
		})
	}
	require.Equal(t, []int64{20, 0, 100, 100, 30}, emitter.values)
}

func TestVMCompactionProactivenessWithoutDynamicConfiguration(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "compaction_proactiveness")
	require.NoError(t, os.WriteFile(path, []byte("20\n"), 0o600))
	missingFragMem := dynamicconfig.NewDynamicAgentConfiguration()
	missingFragMem.GetDynamicConfiguration().FragMemConfiguration = nil
	for _, conf := range []*dynamicconfig.DynamicAgentConfiguration{nil, {}, missingFragMem} {
		emitter := &compactionProactivenessEmitter{}
		require.NoError(t, setVMCompactionProactivenessAtPath(conf, path, emitter))
		require.Equal(t, []int64{20}, emitter.values)
	}
}

func TestSyncVMCompactionProactiveness(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		configured bool
		target     int64
		before     string
		after      string
		wantMetric int64
		wantErr    bool
	}{
		{name: "observe only", before: "\t20\n", after: "\t20\n", wantMetric: 20},
		{name: "observe zero", before: "0\n", after: "0\n"},
		{name: "explicit zero", configured: true, before: "20\n", after: "0\n"},
		{name: "maximum", configured: true, target: 100, before: "20\n", after: "100\n", wantMetric: 100},
		{name: "unchanged", configured: true, target: 20, before: "\t20\n", after: "\t20\n", wantMetric: 20},
		{name: "negative target", configured: true, target: -1, before: "20\n", after: "20\n", wantMetric: 20, wantErr: true},
		{name: "excessive target", configured: true, target: 101, before: "20\n", after: "20\n", wantMetric: 20, wantErr: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "compaction_proactiveness")
			require.NoError(t, os.WriteFile(path, []byte(tc.before), 0o600))
			oldTime := time.Unix(946684800, 0)
			require.NoError(t, os.Chtimes(path, oldTime, oldTime))
			var target *int64
			if tc.configured {
				target = &tc.target
			}
			emitter := &compactionProactivenessEmitter{}
			err := syncVMCompactionProactiveness(path, target, emitter)
			if tc.wantErr {
				require.ErrorContains(t, err, "write vm.compaction_proactiveness")
			} else {
				require.NoError(t, err)
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, tc.after, string(data))
			require.Equal(t, []string{metricNameVMCompactionProactiveness}, emitter.names)
			require.Equal(t, []int64{tc.wantMetric}, emitter.values)
			require.Equal(t, []metrics.MetricTypeName{metrics.MetricTypeNameRaw}, emitter.types)

			if tc.before == tc.after {
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.True(t, oldTime.Equal(info.ModTime()), "observation and unchanged targets must not write")
			}
		})
	}
}

func TestSyncVMCompactionProactivenessMissing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing")
	emitter := &compactionProactivenessEmitter{}
	require.NoError(t, syncVMCompactionProactiveness(path, nil, emitter))
	require.Empty(t, emitter.values)

	target := int64(0)
	require.Error(t, syncVMCompactionProactiveness(path, &target, emitter))
	require.Empty(t, emitter.values)
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "must not create a missing sysctl")
}

func TestSyncVMCompactionProactivenessReadFailure(t *testing.T) {
	t.Parallel()

	for _, content := range []string{"", "invalid\n", "-1\n", "101\n"} {
		content := content
		t.Run(content, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "compaction_proactiveness")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			emitter := &compactionProactivenessEmitter{}
			require.Error(t, syncVMCompactionProactiveness(path, nil, emitter))
			require.Empty(t, emitter.values, "failed reads must not report a synthetic zero")
		})
	}

	emitter := &compactionProactivenessEmitter{}
	require.Error(t, syncVMCompactionProactiveness(t.TempDir(), nil, emitter))
	require.Empty(t, emitter.values)
}

func TestSyncVMCompactionProactivenessWriteFailureStillEmitsActual(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root can write read-only test files")
	}

	path := filepath.Join(t.TempDir(), "compaction_proactiveness")
	require.NoError(t, os.WriteFile(path, []byte("20\n"), 0o600))
	require.NoError(t, os.Chmod(path, 0o400))
	target := int64(0)
	emitter := &compactionProactivenessEmitter{}
	require.ErrorContains(t, syncVMCompactionProactiveness(path, &target, emitter), "write vm.compaction_proactiveness")
	require.Equal(t, []int64{20}, emitter.values)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "20\n", string(data))
}
