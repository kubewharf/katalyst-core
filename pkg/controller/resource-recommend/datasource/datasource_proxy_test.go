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

package datasource

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
)

type MockDatasource struct {
	mock.Mock
}

func (m *MockDatasource) ConvertMetricToQuery(metric datasourcetypes.Metric) (*datasourcetypes.Query, error) {
	args := m.Called(metric)
	return args.Get(0).(*datasourcetypes.Query), args.Error(1)
}

func (m *MockDatasource) QueryTimeSeries(query *datasourcetypes.Query, start, end time.Time, step time.Duration) (*datasourcetypes.TimeSeries, error) {
	args := m.Called(query, start, end, step)
	return args.Get(0).(*datasourcetypes.TimeSeries), args.Error(1)
}

func TestProxy_QueryTimeSeries(t *testing.T) {
	// Create mock objects
	mockDatasource := &MockDatasource{}
	mockTimeSeries := &datasourcetypes.TimeSeries{}

	// Create Proxy with mock datasource
	proxy := &Proxy{
		datasourceMap: map[DatasourceType]Datasource{
			DatasourceType("mock"): mockDatasource,
		},
	}

	// Define test cases
	testCases := []struct {
		name        string
		datasource  DatasourceType
		metric      datasourcetypes.Metric
		start       time.Time
		end         time.Time
		step        time.Duration
		expectedTS  *datasourcetypes.TimeSeries
		expectedErr error
	}{
		{
			name:       "Valid query",
			datasource: "mock",
			metric:     datasourcetypes.Metric{},
			start:      time.Now(),
			end:        time.Now(),
			step:       time.Second,
			expectedTS: mockTimeSeries,
		},
		{
			name:        "Error getting datasource",
			datasource:  "invalid",
			metric:      datasourcetypes.Metric{},
			start:       time.Now(),
			end:         time.Now(),
			step:        time.Second,
			expectedErr: errors.New("datasource not found"),
		},
	}

	// Set up mock behaviors and perform tests
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set up mock behaviors
			mockDatasource.On("ConvertMetricToQuery", tc.metric).Return(&datasourcetypes.Query{}, nil)
			if tc.expectedErr == nil {
				mockDatasource.On("QueryTimeSeries", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(mockTimeSeries, nil)
			} else {
				mockDatasource.On("QueryTimeSeries", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, tc.expectedErr)
			}

			// Call the QueryTimeSeries method
			result, err := proxy.QueryTimeSeries(tc.datasource, tc.metric, tc.start, tc.end, tc.step)

			// Verify the results
			assert.Equal(t, tc.expectedTS, result)
			assert.Equal(t, tc.expectedErr, err)

			// Verify mock invocations
			mockDatasource.AssertExpectations(t)
		})
	}
}
