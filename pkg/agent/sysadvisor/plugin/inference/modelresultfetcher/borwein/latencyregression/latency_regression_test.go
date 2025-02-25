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
		PredictValue:     0.6,
		EquilibriumValue: 0.1,
		Ignore:           false,
	}
	bs1, _ := json.Marshal(res1)
	res2 := &LatencyRegression{
		PredictValue:     0.7,
		EquilibriumValue: 0.1,
		Ignore:           true,
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
						PredictValue:     0.6,
						EquilibriumValue: 0.1,
						Ignore:           false,
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
