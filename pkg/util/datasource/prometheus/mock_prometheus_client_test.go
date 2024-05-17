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

package prometheus

import (
	"context"
	"testing"
	"time"

	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
)

func TestMockPromAPIClient(t *testing.T) {
	t.Parallel()

	t.Run("normal", func(t *testing.T) {
		t.Parallel()

		ctx := context.TODO()
		m := MockPromAPIClient{
			QueryRangeFunc: func(ctx context.Context, query string, r v1.Range) (model.Value, v1.Warnings, error) {
				return nil, nil, nil
			},
		}

		var warningsExpected v1.Warnings

		alertResult, err := m.Alerts(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.AlertsResult{}, alertResult)

		rulesResult, err := m.Rules(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.RulesResult{}, rulesResult)

		alertManagersResult, err := m.AlertManagers(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.AlertManagersResult{}, alertManagersResult)

		assert.Nil(t, m.CleanTombstones(ctx))
		assert.Nil(t, m.DeleteSeries(ctx, nil, time.Time{}, time.Time{}))

		config, err := m.Config(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.ConfigResult{}, config)

		flags, err := m.Flags(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.FlagsResult(nil), flags)

		strs, warnings, err := m.LabelNames(ctx, nil, time.Time{}, time.Time{})
		assert.Equal(t, nil, err)
		assert.Equal(t, []string(nil), strs)
		assert.Equal(t, warningsExpected, warnings)

		labelValues, warnings, err := m.LabelValues(ctx, "", nil, time.Time{}, time.Time{})
		assert.Equal(t, nil, err)
		assert.Equal(t, model.LabelValues(nil), labelValues)
		assert.Equal(t, warningsExpected, warnings)

		modelValue, warnings, err := m.Query(ctx, "", time.Time{})
		assert.Equal(t, nil, err)
		assert.Equal(t, nil, modelValue)
		assert.Equal(t, warningsExpected, warnings)

		exemplarQueryResults, err := m.QueryExemplars(ctx, "", time.Time{}, time.Time{})
		assert.Equal(t, nil, err)
		assert.Equal(t, []v1.ExemplarQueryResult(nil), exemplarQueryResults)

		buildinfoResult, err := m.Buildinfo(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.BuildinfoResult{}, buildinfoResult)

		runtimeinfoResult, err := m.Runtimeinfo(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.RuntimeinfoResult{}, runtimeinfoResult)

		labelSets, warnings, err := m.Series(ctx, nil, time.Time{}, time.Time{})
		assert.Equal(t, nil, err)
		assert.Equal(t, []model.LabelSet(nil), labelSets)
		assert.Equal(t, warningsExpected, warnings)

		snapshotResult, err := m.Snapshot(ctx, false)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.SnapshotResult{}, snapshotResult)

		targetsResult, err := m.Targets(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.TargetsResult{}, targetsResult)

		metricMetadata, err := m.TargetsMetadata(ctx, "", "", "")
		assert.Equal(t, nil, err)
		assert.Equal(t, []v1.MetricMetadata(nil), metricMetadata)

		metadata, err := m.Metadata(ctx, "", "")
		assert.Equal(t, nil, err)
		assert.Equal(t, map[string][]v1.Metadata(nil), metadata)

		tsdbResult, err := m.TSDB(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.TSDBResult{}, tsdbResult)

		walReplayStatus, err := m.WalReplay(ctx)
		assert.Equal(t, nil, err)
		assert.Equal(t, v1.WalReplayStatus{}, walReplayStatus)

		modelValue, warnings, err = m.QueryRange(ctx, "", v1.Range{})
		assert.Equal(t, nil, err)
		assert.Equal(t, warningsExpected, warnings)
		assert.Equal(t, nil, modelValue)
	})
}
