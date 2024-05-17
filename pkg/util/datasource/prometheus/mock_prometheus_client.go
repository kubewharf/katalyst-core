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

// MockPromAPIClient is a mock implementation of v1.API for testing purposes.
type MockPromAPIClient struct {
	QueryRangeFunc func(ctx context.Context, query string, r v1.Range) (model.Value, v1.Warnings, error)
}

func (m *MockPromAPIClient) Alerts(ctx context.Context) (v1.AlertsResult, error) {
	return v1.AlertsResult{}, nil
}

func (m *MockPromAPIClient) AlertManagers(ctx context.Context) (v1.AlertManagersResult, error) {
	return v1.AlertManagersResult{}, nil
}

func (m *MockPromAPIClient) CleanTombstones(ctx context.Context) error {
	return nil
}

func (m *MockPromAPIClient) Config(ctx context.Context) (v1.ConfigResult, error) {
	return v1.ConfigResult{}, nil
}

func (m *MockPromAPIClient) DeleteSeries(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) error {
	return nil
}

func (m *MockPromAPIClient) Flags(ctx context.Context) (v1.FlagsResult, error) {
	return nil, nil
}

func (m *MockPromAPIClient) LabelNames(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) ([]string, v1.Warnings, error) {
	return nil, nil, nil
}

func (m *MockPromAPIClient) LabelValues(ctx context.Context, label string, matches []string, startTime time.Time, endTime time.Time) (model.LabelValues, v1.Warnings, error) {
	return nil, nil, nil
}

func (m *MockPromAPIClient) Query(ctx context.Context, query string, ts time.Time, opts ...v1.Option) (model.Value, v1.Warnings, error) {
	return nil, nil, nil
}

func (m *MockPromAPIClient) QueryExemplars(ctx context.Context, query string, startTime time.Time, endTime time.Time) ([]v1.ExemplarQueryResult, error) {
	return nil, nil
}

func (m *MockPromAPIClient) Buildinfo(ctx context.Context) (v1.BuildinfoResult, error) {
	return v1.BuildinfoResult{}, nil
}

func (m *MockPromAPIClient) Runtimeinfo(ctx context.Context) (v1.RuntimeinfoResult, error) {
	return v1.RuntimeinfoResult{}, nil
}

func (m *MockPromAPIClient) Series(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) ([]model.LabelSet, v1.Warnings, error) {
	return nil, nil, nil
}

func (m *MockPromAPIClient) Snapshot(ctx context.Context, skipHead bool) (v1.SnapshotResult, error) {
	return v1.SnapshotResult{}, nil
}

func (m *MockPromAPIClient) Rules(ctx context.Context) (v1.RulesResult, error) {
	return v1.RulesResult{}, nil
}

func (m *MockPromAPIClient) Targets(ctx context.Context) (v1.TargetsResult, error) {
	return v1.TargetsResult{}, nil
}

func (m *MockPromAPIClient) TargetsMetadata(ctx context.Context, matchTarget string, metric string, limit string) ([]v1.MetricMetadata, error) {
	return nil, nil
}

func (m *MockPromAPIClient) Metadata(ctx context.Context, metric string, limit string) (map[string][]v1.Metadata, error) {
	return nil, nil
}

func (m *MockPromAPIClient) TSDB(ctx context.Context) (v1.TSDBResult, error) {
	return v1.TSDBResult{}, nil
}

func (m *MockPromAPIClient) WalReplay(ctx context.Context) (v1.WalReplayStatus, error) {
	return v1.WalReplayStatus{}, nil
}

func (m *MockPromAPIClient) QueryRange(ctx context.Context, query string, r v1.Range, opts ...v1.Option) (model.Value, v1.Warnings, error) {
	return m.QueryRangeFunc(ctx, query, r)
}
