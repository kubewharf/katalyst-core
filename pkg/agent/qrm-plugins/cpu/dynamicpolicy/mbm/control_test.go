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

package mbm

import (
	"reflect"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/mbw/monitor"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/external/mbm"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type mockStater struct {
	NUMAStater
}

func (m mockStater) GetMachineState() state.NUMANodeMap {
	return state.NUMANodeMap{
		// package 1
		3: {PodEntries: map[string]state.ContainerEntries{"pod-foo": {}}},
		4: {PodEntries: map[string]state.ContainerEntries{"pod-bar": {}}},
		5: {PodEntries: map[string]state.ContainerEntries{"pod-foo": {}}},
		// package 2
		6: {PodEntries: map[string]state.ContainerEntries{"pod-foo": {}}},
		7: {PodEntries: map[string]state.ContainerEntries{"pod-bar": {}}},
		8: {PodEntries: map[string]state.ContainerEntries{"pod-baz": {}}},
	}
}

func TestController_getActiveNodeSets(t *testing.T) {
	t.Parallel()
	type fields struct {
		numaStater NUMAStater
	}
	type args struct {
		nodes []int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[string]sets.Int
	}{
		{
			name: "happy path of mixed nodes",
			fields: fields{
				numaStater: &mockStater{},
			},
			args: args{
				nodes: []int{3, 4, 5},
			},
			want: map[string]sets.Int{
				"pod-bar": {4: {}},
				"pod-foo": {3: {}, 5: {}},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := Controller{
				numaStater: tt.fields.numaStater,
			}
			if got := c.getActiveNodeSets(tt.args.nodes); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getActiveNodeSets() = %v, want %v", got, tt.want)
			}
		})
	}
}

type mockMBReader struct {
	types.MetricsReader
}

func (m mockMBReader) GetNumaMetric(numaID int, metricName string) (metric.MetricData, error) {
	if numaID == 0 || numaID == 1 || numaID == 2 {
		return metric.MetricData{
			Value: 9.0 + float64(numaID*10),
		}, nil
	}
	if numaID == 3 || numaID == 4 || numaID == 5 {
		return metric.MetricData{
			Value: 11.1,
		}, nil
	}
	if numaID == 6 || numaID == 7 || numaID == 8 {
		return metric.MetricData{
			Value: float64(numaID * 10),
		}, nil
	}
	return metric.MetricData{}, errors.New("test error")
}

func (m mockMBReader) GetPackageMetric(packageID int, metricName string) (metric.MetricData, error) {
	if packageID == 2 {
		return metric.MetricData{
			Value: 90,
		}, nil
	}

	return metric.MetricData{
		Value: 20,
	}, nil
}

func TestController_getMBMetrics(t *testing.T) {
	t.Parallel()
	type fields struct {
		metricEmitter      metrics.MetricEmitter
		metricReader       types.MetricsReader
		numaStater         NUMAStater
		mbAdjust           mbm.MBAdjuster
		packageMap         map[int][]int
		interval           time.Duration
		bandwidthThreshold int64
	}
	type args struct {
		actives map[string]sets.Int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    []map[int]float64
		wantErr bool
	}{
		{
			name: "happy path to get mixed node group MB metrics",
			fields: fields{
				metricReader: &mockMBReader{},
			},
			args: args{
				actives: map[string]sets.Int{
					"pod0": {1: {}, 0: {}},
					"pod2": {2: {}},
				},
			},
			want:    []map[int]float64{{2: 11.1}, {0: 11.1, 1: 11.1}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := Controller{
				metricEmitter:      tt.fields.metricEmitter,
				metricReader:       tt.fields.metricReader,
				numaStater:         tt.fields.numaStater,
				mbAdjust:           tt.fields.mbAdjust,
				packageMap:         tt.fields.packageMap,
				interval:           tt.fields.interval,
				bandwidthThreshold: tt.fields.bandwidthThreshold,
			}
			got, err := c.getGroupMBMetrics(tt.args.actives)
			if (err != nil) != tt.wantErr {
				t.Errorf("getGroupMBMetrics() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Equal(t, len(got), len(tt.want))
		})
	}
}

type dummyMBAdjuster struct {
	mbm.MBAdjuster
	calledToReduce     bool
	calledToRaise      bool
	calledToUnthrottle bool
	sharesToAdjust     map[int]uint64
}

func (d *dummyMBAdjuster) AdjustNumaMB(node int, avgMB, quota uint64, action monitor.MB_CONTROL_ACTION) error {
	switch action {
	case monitor.MEMORY_BANDWIDTH_CONTROL_REDUCE:
		d.calledToReduce = true
	case monitor.MEMORY_BANDWIDTH_CONTROL_RAISE:
		d.calledToRaise = true
	case monitor.MEMORY_BANDWIDTH_CONTROL_UNTHROTTLE:
		d.calledToUnthrottle = true
	}
	d.sharesToAdjust[node] = quota
	return nil
}

func TestController_processPackage_reduce(t *testing.T) {
	t.Parallel()
	dummyAdjuster := &dummyMBAdjuster{
		sharesToAdjust: map[int]uint64{},
	}
	controller := Controller{
		metricReader:       &mockMBReader{},
		numaStater:         &mockStater{},
		mbAdjust:           dummyAdjuster,
		bandwidthThreshold: 15,
		minDeductionStep:   1,
		numaThrottled:      sets.Int{},
	}

	controller.processPackage(1, []int{3, 4, 5})

	if !dummyAdjuster.calledToReduce {
		t.Errorf("expected to reduce MB on node, and not")
	}

	assert.Equal(t, controller.numaThrottled, sets.Int{3: {}, 4: {}, 5: {}})
}

func TestController_processPackage_unthrottle(t *testing.T) {
	t.Parallel()
	dummyAdjuster := &dummyMBAdjuster{
		sharesToAdjust: map[int]uint64{},
	}
	controller := Controller{
		metricReader:       &mockMBReader{},
		numaStater:         &mockStater{},
		mbAdjust:           dummyAdjuster,
		bandwidthThreshold: 150,
		minIncreaseStep:    1,
		numaThrottled:      sets.Int{0: {}, 2: {}},
	}

	controller.processPackage(0, []int{0, 1, 2})

	if !dummyAdjuster.calledToUnthrottle {
		t.Errorf("expected to unthrottle MB on all nodes, and not")
	}
	assert.Equal(t, sets.Int{}, controller.numaThrottled)
}

func TestController_processPackage_raise(t *testing.T) {
	t.Parallel()
	dummyAdjuster := &dummyMBAdjuster{
		sharesToAdjust: map[int]uint64{},
	}
	controller := Controller{
		metricReader:       &mockMBReader{},
		numaStater:         &mockStater{},
		mbAdjust:           dummyAdjuster,
		bandwidthThreshold: 100,
		minIncreaseStep:    1,
		numaThrottled:      sets.Int{6: {}, 8: {}},
	}

	controller.processPackage(2, []int{6, 7, 8})

	if !dummyAdjuster.calledToRaise {
		t.Errorf("expected to raise MB on node, and not")
	}
	assert.Equal(t, sets.Int{6: {}, 8: {}}, controller.numaThrottled)
	assert.Equal(t, map[int]uint64{6: 6, 8: 5}, dummyAdjuster.sharesToAdjust)
}
