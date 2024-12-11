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

package node

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type testResp struct {
	tags []metrics.MetricTag
	res  float64
}

type testMetrics struct {
	res chan testResp
	metrics.DummyMetrics
}

func (t testMetrics) StoreFloat64(_ string, data float64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	t.res <- testResp{
		res:  data,
		tags: tags,
	}
	return nil
}

func TestMetricSyncerNode_receiveRawNodeNUMA(t *testing.T) {
	t.Parallel()

	testTimestamp := time.Date(2024, 11, 12, 0, 0, 0, 0, time.Local)
	testUnixTime := testTimestamp.UnixMilli()

	type fields struct {
		numaMetricMapping map[string]string
	}
	type args struct {
		resp metrictypes.NotifiedResponse
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   testResp
	}{
		{
			name: "test",
			fields: fields{
				numaMetricMapping: nodeNUMARawMetricNameMapping,
			},
			args: args{
				resp: metrictypes.NotifiedResponse{
					MetricData: metric.MetricData{
						Value: 100,
						Time:  &testTimestamp,
					},
					Req: metrictypes.NotifiedRequest{
						MetricName: consts.MetricMemBandwidthReadNuma,
						NumaID:     0,
					},
				},
			},
			want: testResp{
				tags: []metrics.MetricTag{
					{
						Key: "object",
						Val: "nodes",
					},
					{
						Key: "object_name",
						Val: "",
					},
					{
						Key: "timestamp",
						Val: strconv.Itoa(int(testUnixTime)),
					},
					{
						Key: "selector_numa",
						Val: "0",
					},
				},
				res: 100,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := make(chan testResp)
			defer close(res)
			n := &MetricSyncerNode{
				numaMetricMapping: tt.fields.numaMetricMapping,
				dataEmitter:       testMetrics{res: res},
				node:              &v1.Node{},
			}
			rChan := make(chan metrictypes.NotifiedResponse)
			defer close(rChan)
			go n.receiveRawNodeNUMA(context.TODO(), rChan)
			go func() {
				rChan <- tt.args.resp
			}()
			result := <-res
			assert.Equal(t, tt.want, result)
		})
	}
}
