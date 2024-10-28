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

package controller

import (
	"testing"
	"time"

	"github.com/bytedance/mockey"
	promapiv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	resourcerecommendprometheus "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource/prometheus"
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

func (m *MockDatasource) GetPromClient() promapiv1.API {
	args := m.Called()
	return args.Get(0).(promapiv1.API)
}

func Test_initDataSources(t *testing.T) {
	proxy := datasource.NewProxy()
	mockDatasource := MockDatasource{}
	proxy.RegisterDatasource(datasource.PrometheusDatasource, &mockDatasource)
	type args struct {
		opts *controller.ResourceRecommenderConfig
	}
	tests := []struct {
		name string
		args args
		want *datasource.Proxy
	}{
		{
			name: "return_Datasource",
			args: args{
				opts: &controller.ResourceRecommenderConfig{
					DataSource: []string{string(datasource.PrometheusDatasource)},
				},
			},
			want: proxy,
		},
	}
	for _, tt := range tests {
		defer mockey.UnPatchAll()
		mockey.PatchConvey(tt.name, t, func() {
			mockey.Mock(resourcerecommendprometheus.NewPrometheus).Return(&mockDatasource, nil).Build()

			got := initDataSources(tt.args.opts)
			convey.So(got, convey.ShouldResemble, tt.want)
		})
	}
}
