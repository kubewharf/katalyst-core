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

package latencyregression

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	borweinutils "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/utils"
)

func TestGetLatencyRegressionPredictValue(t *testing.T) {
	t.Parallel()
	timeNow := time.Now().Unix()
	res1 := &LatencyRegression{
		PredictValue: 0.6,
		Ignore:       false,
	}
	bs1, _ := json.Marshal(res1)
	res2 := &LatencyRegression{
		PredictValue: 0.7,
		Ignore:       true,
	}
	bs2, _ := json.Marshal(res2)
	reader := metacache.NewDummyMetaCacheImp()
	_ = reader.SetInferenceResult(borweinutils.GetInferenceResultKey(borweinconsts.ModelNameBorweinLatencyRegression), &borweintypes.BorweinInferenceResults{
		Timestamp: timeNow,
		Results: map[string]map[string][]*borweininfsvc.InferenceResult{
			"test1": {
				"test": []*borweininfsvc.InferenceResult{
					{
						GenericOutput: string(bs1),
					},
				},
			},
			"test2": {
				"test": []*borweininfsvc.InferenceResult{
					{
						GenericOutput: string(bs2),
					},
				},
			},
		},
	})
	type args struct {
		metaReader metacache.MetaReader
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]map[string]*LatencyRegression
		wantTs  int64
		wantErr bool
	}{
		{
			name:    "GetLatencyRegressionPredictValue failed",
			want:    nil,
			wantTs:  0,
			wantErr: true,
		},
		{
			name: "GetLatencyRegressionPredictValue success",
			args: args{
				metaReader: reader,
			},
			want: map[string]map[string]*LatencyRegression{
				"test1": {
					"test": &LatencyRegression{
						PredictValue: 0.6,
						Ignore:       false,
					},
				},
			},
			wantTs:  timeNow,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		curTT := tt
		t.Run(curTT.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := GetLatencyRegressionPredictResult(curTT.args.metaReader, false, nil)
			if (err != nil) != curTT.wantErr {
				t.Errorf("GetLatencyRegressionPredictResult() error = %v, wantErr %v", err, curTT.wantErr)
				return
			}
			if !reflect.DeepEqual(got, curTT.want) {
				t.Errorf("GetLatencyRegressionPredictResult() got = %v, want %v", got, curTT.want)
			}
		})
	}
}

func TestInSpecialTime(t *testing.T) {
	t.Parallel()

	now := time.Now()
	currentHour := now.Hour()
	currentMinute := now.Minute()

	inRangeStartHour := currentHour
	inRangeStartMinute := currentMinute - 10
	if inRangeStartMinute < 0 {
		inRangeStartMinute += 60
		inRangeStartHour--
	}
	inRangeEndHour := currentHour
	inRangeEndMinute := currentMinute + 10
	if inRangeEndMinute >= 60 {
		inRangeEndMinute -= 60
		inRangeEndHour++
	}
	inRangeStartStr := fmt.Sprintf("%02d:%02d", inRangeStartHour, inRangeStartMinute)
	inRangeEndStr := fmt.Sprintf("%02d:%02d", inRangeEndHour, inRangeEndMinute)

	outOfRangeStartHour := currentHour + 1
	outOfRangeStartMinute := currentMinute
	if outOfRangeStartMinute >= 60 {
		outOfRangeStartMinute -= 60
		outOfRangeStartHour++
	}
	outOfRangeEndHour := outOfRangeStartHour + 1
	outOfRangeEndMinute := currentMinute
	if outOfRangeEndMinute >= 60 {
		outOfRangeEndMinute -= 60
		outOfRangeEndHour++
	}
	outOfRangeStartStr := fmt.Sprintf("%02d:%02d", outOfRangeStartHour, outOfRangeStartMinute)
	outOfRangeEndStr := fmt.Sprintf("%02d:%02d", outOfRangeEndHour, outOfRangeEndMinute)

	tests := []struct {
		name        string
		specialTime StrategySpecialTimeSlot
		wantOffset  float64
		wantResult  bool
	}{
		{
			name: "In range",
			specialTime: StrategySpecialTimeSlot{
				TimeRange: []string{inRangeStartStr, inRangeEndStr},
				Offset:    1.5,
			},
			wantOffset: 1.5,
			wantResult: true,
		},
		{
			name: "Out of range",
			specialTime: StrategySpecialTimeSlot{
				TimeRange: []string{outOfRangeStartStr, outOfRangeEndStr},
				Offset:    2.0,
			},
			wantOffset: 0,
			wantResult: false,
		},
		{
			name: "Invalid time range length",
			specialTime: StrategySpecialTimeSlot{
				TimeRange: []string{"14:00"},
				Offset:    2.5,
			},
			wantOffset: 0,
			wantResult: false,
		},
	}

	for _, tt := range tests {
		curTT := tt
		t.Run(curTT.name, func(t *testing.T) {
			t.Parallel()
			gotOffset, gotResult := InSpecialTime(curTT.specialTime)
			if !reflect.DeepEqual(gotOffset, curTT.wantOffset) {
				t.Errorf("InSpecialTime() gotOffset = %v, want %v", gotOffset, curTT.wantOffset)
			}
			if gotResult != curTT.wantResult {
				t.Errorf("InSpecialTime() gotResult = %v, want %v", gotResult, curTT.wantResult)
			}
		})
	}
}

func TestParseStrategy(t *testing.T) {
	t.Parallel()

	jsonStr := `{
       "strategy_slots": {
           "cpu_usage_ratio": [
           {
				"slot": 5,
                "net": -1.1,
				"offset": -0.15
           },
           {
                "slot": 20,
                "net": -0.5,
				"offset": -0.08
           },
           {
                "slot": 50,
                "net": 0.2,
				"offset": 0.07
           },
           {
                "slot": 90,
                "net": 1.1,
				"offset": 0.18
           }
       ]
       },
       "strategy_special_time": {
         "cpu_usage_ratio": [
			{
				"time_range": [
					"20:30",
					"21:00"
				],
				"offset": -0.15
			},
			{
				"time_range": [
					"21:00",
					"22:00"
				],
				"offset": -0.2
			}
		]
       }
   }`
	type args struct {
		strategyParam string
	}
	tests := []struct {
		name    string
		args    args
		want    BorweinStrategy
		wantErr bool
	}{
		{
			name: "test1",
			args: args{
				strategyParam: jsonStr,
			},
			want: BorweinStrategy{
				StrategySlots: ClusterStrategy{
					"cpu_usage_ratio": StrategySlots{
						{
							Slot:   5,
							NET:    -1.1,
							Offset: -0.15,
						},
						{
							Slot:   20,
							NET:    -0.5,
							Offset: -0.08,
						},
						{
							Slot:   50,
							NET:    0.2,
							Offset: 0.07,
						},
						{
							Slot:   90,
							NET:    1.1,
							Offset: 0.18,
						},
					},
				},
				StrategySpecialTime: ClusterSpecialTimeStrategy{
					"cpu_usage_ratio": []StrategySpecialTimeSlot{
						{
							TimeRange: []string{"20:30", "21:00"},
							Offset:    -0.15,
						},
						{
							TimeRange: []string{"21:00", "22:00"},
							Offset:    -0.2,
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		curTT := tt
		t.Run(curTT.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseStrategy(curTT.args.strategyParam)
			if (err != nil) != curTT.wantErr {
				t.Errorf("ParseStrategy() error = %v, wantErr %v", err, curTT.wantErr)
				return
			}
			if !reflect.DeepEqual(got, curTT.want) {
				t.Errorf("ParseStrategy() got = %v, want %v", got, curTT.want)
			}
		})
	}
}

func TestMatchSlotOffset(t *testing.T) {
	t.Parallel()

	type args struct {
		nodeAvgNet       float64
		clusterStrategy  ClusterStrategy
		indicator        string
		borweinParameter *borweintypes.BorweinParameter
	}
	tests := []struct {
		name string
		args args
		want float64
	}{
		{
			name: "nodeAvgNet in middle range, matches corresponding slot",
			args: args{
				nodeAvgNet: 1.0,
				clusterStrategy: ClusterStrategy{
					"cpu_usage_ratio": StrategySlots{
						{
							Slot:   5,
							NET:    -1.1,
							Offset: -0.15,
						},
						{
							Slot:   20,
							NET:    -0.5,
							Offset: -0.08,
						},
						{
							Slot:   50,
							NET:    0.2,
							Offset: 0.07,
						},
						{
							Slot:   90,
							NET:    1.1,
							Offset: 0.18,
						},
					},
				},
				indicator: "cpu_usage_ratio",
				borweinParameter: &borweintypes.BorweinParameter{
					OffsetMin: -0.2,
					OffsetMax: 0.2,
				},
			},
			want: 0.18,
		},
		{
			name: "nodeAvgNet less than all slot NET, matches first slot",
			args: args{
				nodeAvgNet: -1.3,
				clusterStrategy: ClusterStrategy{
					"cpu_usage_ratio": StrategySlots{
						{
							Slot:   5,
							NET:    -1.1,
							Offset: -0.15,
						},
						{
							Slot:   20,
							NET:    -0.5,
							Offset: -0.08,
						},
						{
							Slot:   50,
							NET:    0.2,
							Offset: 0.07,
						},
						{
							Slot:   90,
							NET:    1.1,
							Offset: 0.18,
						},
					},
				},
				indicator: "cpu_usage_ratio",
				borweinParameter: &borweintypes.BorweinParameter{
					OffsetMin: -0.2,
					OffsetMax: 0.2,
				},
			},
			want: -0.15,
		},
		{
			name: "indicator not exists, returns 0",
			args: args{
				nodeAvgNet: -1.3,
				clusterStrategy: ClusterStrategy{
					"cpu_usage_ratio": StrategySlots{
						{
							Slot:   5,
							NET:    -1.1,
							Offset: -0.15,
						},
					},
				},
				indicator: "cpu_sched_wait",
				borweinParameter: &borweintypes.BorweinParameter{
					OffsetMin: -0.2,
					OffsetMax: 0.2,
				},
			},
			want: 0,
		},
		{
			name: "nodeAvgNet greater than all slot NET, returns last slot offset",
			args: args{
				nodeAvgNet: 2.1,
				clusterStrategy: ClusterStrategy{
					"cpu_usage_ratio": StrategySlots{
						{
							Slot:   5,
							NET:    -1.1,
							Offset: -0.15,
						},
						{
							Slot:   20,
							NET:    -0.5,
							Offset: -0.08,
						},
						{
							Slot:   50,
							NET:    0.2,
							Offset: 0.07,
						},
						{
							Slot:   90,
							NET:    1.1,
							Offset: 0.18,
						},
					},
				},
				indicator: "cpu_usage_ratio",
				borweinParameter: &borweintypes.BorweinParameter{
					OffsetMin: -0.2,
					OffsetMax: 0.2,
				},
			},
			want: 0.18,
		},
		{
			name: "slots is empty, returns 0",
			args: args{
				nodeAvgNet: 1.0,
				clusterStrategy: ClusterStrategy{
					"cpu_usage_ratio": StrategySlots{},
				},
				indicator: "cpu_usage_ratio",
				borweinParameter: &borweintypes.BorweinParameter{
					OffsetMin: -0.2,
					OffsetMax: 0.2,
				},
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchSlotOffset(tt.args.nodeAvgNet, tt.args.clusterStrategy, tt.args.indicator, tt.args.borweinParameter); got != tt.want {
				t.Errorf("MatchSlotOffset() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchSpecialTimes(t *testing.T) {
	t.Parallel()

	type args struct {
		clusterSpecialTimeStrategy ClusterSpecialTimeStrategy
		indicator                  string
	}
	tests := []struct {
		name      string
		args      args
		want      float64
		wantMatch bool
	}{
		{},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, got1 := MatchSpecialTimes(tt.args.clusterSpecialTimeStrategy, tt.args.indicator)
			if got != tt.want {
				t.Errorf("MatchSpecialTimes() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.wantMatch {
				t.Errorf("MatchSpecialTimes() got1 = %v, want %v", got1, tt.wantMatch)
			}
		})
	}
}
