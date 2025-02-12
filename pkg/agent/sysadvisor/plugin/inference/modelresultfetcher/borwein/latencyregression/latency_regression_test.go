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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	borweinutils "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/utils"
	"reflect"
	"testing"
	"time"
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
			got, _, err := GetLatencyRegressionPredictResult(curTT.args.metaReader)
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
	now := time.Now()
	currentHour := now.Hour()
	currentMinute := now.Minute()

	// 构建在范围内的时间范围
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

	// 构建不在范围内的时间范围
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
		specialTime StrategySpecialTime
		wantOffset  float64
		wantResult  bool
	}{
		{
			name: "In range",
			specialTime: StrategySpecialTime{
				TimeRange: []string{inRangeStartStr, inRangeEndStr},
				Offset:    1.5,
			},
			wantOffset: 1.5,
			wantResult: true,
		},
		{
			name: "Out of range",
			specialTime: StrategySpecialTime{
				TimeRange: []string{outOfRangeStartStr, outOfRangeEndStr},
				Offset:    2.0,
			},
			wantOffset: 0,
			wantResult: false,
		},
		{
			name: "Invalid time range length",
			specialTime: StrategySpecialTime{
				TimeRange: []string{"14:00"},
				Offset:    2.5,
			},
			wantOffset: 0,
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOffset, gotResult := InSpecialTime(tt.specialTime)
			if gotOffset != tt.wantOffset {
				t.Errorf("InSpecialTime() gotOffset = %v, want %v", gotOffset, tt.wantOffset)
			}
			if gotResult != tt.wantResult {
				t.Errorf("InSpecialTime() gotResult = %v, want %v", gotResult, tt.wantResult)
			}
		})
	}
}

func TestParseStrategy(t *testing.T) {
	jsonStr := `{
        "strategy_slots": [
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
        ],
        "strategy_special_time": {
            "time_range": [
                "20:30",
                "22:00"
            ],
            "offset": -0.2
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
				StrategySlots: []StrategySlot{
					{
						Slot:   5,
						Net:    -1.1,
						Offset: -0.15,
					},
					{
						Slot:   20,
						Net:    -0.5,
						Offset: -0.08,
					},
					{
						Slot:   50,
						Net:    0.2,
						Offset: 0.07,
					},
					{
						Slot:   90,
						Net:    1.1,
						Offset: 0.18,
					},
				},
				StrategySpecialTime: StrategySpecialTime{
					TimeRange: []string{"20:30", "22:00"},
					Offset:    -0.2,
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStrategy(tt.args.strategyParam)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseStrategy() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseStrategy() got = %v, want %v", got, tt.want)
			}
		})
	}
}
