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
	"time"

	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
)

type DatasourceType string

const (
	PrometheusDatasource DatasourceType = "Prometheus"
)

type Datasource interface {
	QueryTimeSeries(query *datasourcetypes.Query, start time.Time, end time.Time, step time.Duration) (*datasourcetypes.TimeSeries, error)
	ConvertMetricToQuery(metric datasourcetypes.Metric) (*datasourcetypes.Query, error)
}

type Proxy struct {
	datasourceMap map[DatasourceType]Datasource
}

func NewProxy() *Proxy {
	return &Proxy{
		datasourceMap: make(map[DatasourceType]Datasource),
	}
}

func (p *Proxy) RegisterDatasource(name DatasourceType, datasource Datasource) {
	p.datasourceMap[name] = datasource
}

func (p *Proxy) getDatasource(name DatasourceType) (Datasource, error) {
	if datasource, ok := p.datasourceMap[name]; ok {
		return datasource, nil
	}
	return nil, errors.New("datasource not found")
}

func (p *Proxy) QueryTimeSeries(DatasourceName DatasourceType, metric datasourcetypes.Metric, start time.Time, end time.Time, step time.Duration) (*datasourcetypes.TimeSeries, error) {
	datasource, err := p.getDatasource(DatasourceName)
	if err != nil {
		return nil, err
	}
	query, err := datasource.ConvertMetricToQuery(metric)
	if err != nil {
		return nil, err
	}
	return datasource.QueryTimeSeries(query, start, end, step)
}
