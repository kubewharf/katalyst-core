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
	"time"

	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// mockPromAPIClient is a mock implementation of v1.API for testing purposes.
type mockPromAPIClient struct {
	QueryRangeFunc func(ctx context.Context, query string, r v1.Range) (model.Value, v1.Warnings, error)
}

func (m *mockPromAPIClient) Alerts(ctx context.Context) (v1.AlertsResult, error) {
	return v1.AlertsResult{}, nil
}

func (m *mockPromAPIClient) AlertManagers(ctx context.Context) (v1.AlertManagersResult, error) {
	return v1.AlertManagersResult{}, nil
}

func (m *mockPromAPIClient) CleanTombstones(ctx context.Context) error {
	return nil
}

func (m *mockPromAPIClient) Config(ctx context.Context) (v1.ConfigResult, error) {
	return v1.ConfigResult{}, nil
}

func (m *mockPromAPIClient) DeleteSeries(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) error {
	return nil
}

func (m *mockPromAPIClient) Flags(ctx context.Context) (v1.FlagsResult, error) {
	return nil, nil
}

func (m *mockPromAPIClient) LabelNames(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) ([]string, v1.Warnings, error) {
	return nil, nil, nil
}

func (m *mockPromAPIClient) LabelValues(ctx context.Context, label string, matches []string, startTime time.Time, endTime time.Time) (model.LabelValues, v1.Warnings, error) {
	return nil, nil, nil
}

func (m *mockPromAPIClient) Query(ctx context.Context, query string, ts time.Time, opts ...v1.Option) (model.Value, v1.Warnings, error) {
	return nil, nil, nil
}

func (m *mockPromAPIClient) QueryExemplars(ctx context.Context, query string, startTime time.Time, endTime time.Time) ([]v1.ExemplarQueryResult, error) {
	return nil, nil
}

func (m *mockPromAPIClient) Buildinfo(ctx context.Context) (v1.BuildinfoResult, error) {
	return v1.BuildinfoResult{}, nil
}

func (m *mockPromAPIClient) Runtimeinfo(ctx context.Context) (v1.RuntimeinfoResult, error) {
	return v1.RuntimeinfoResult{}, nil
}

func (m *mockPromAPIClient) Series(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) ([]model.LabelSet, v1.Warnings, error) {
	return nil, nil, nil
}

func (m *mockPromAPIClient) Snapshot(ctx context.Context, skipHead bool) (v1.SnapshotResult, error) {
	return v1.SnapshotResult{}, nil
}

func (m *mockPromAPIClient) Rules(ctx context.Context) (v1.RulesResult, error) {
	return v1.RulesResult{}, nil
}

func (m *mockPromAPIClient) Targets(ctx context.Context) (v1.TargetsResult, error) {
	return v1.TargetsResult{}, nil
}

func (m *mockPromAPIClient) TargetsMetadata(ctx context.Context, matchTarget string, metric string, limit string) ([]v1.MetricMetadata, error) {
	return nil, nil
}

func (m *mockPromAPIClient) Metadata(ctx context.Context, metric string, limit string) (map[string][]v1.Metadata, error) {
	return nil, nil
}

func (m *mockPromAPIClient) TSDB(ctx context.Context) (v1.TSDBResult, error) {
	return v1.TSDBResult{}, nil
}

func (m *mockPromAPIClient) WalReplay(ctx context.Context) (v1.WalReplayStatus, error) {
	return v1.WalReplayStatus{}, nil
}

func (m *mockPromAPIClient) QueryRange(ctx context.Context, query string, r v1.Range, opts ...v1.Option) (model.Value, v1.Warnings, error) {
	return m.QueryRangeFunc(ctx, query, r)
}
