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

package trainingtpreg

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

func TestGetTrainingTHRegPredictValue(t *testing.T) {
	t.Parallel()
	timeNow := time.Now().Unix()
	res := &TrainingThroughputRegression{
		PredictValue: 0.5,
	}
	bs, _ := json.Marshal(res)
	reader := metacache.NewDummyMetaCacheImp()
	reader.SetInferenceResult(borweinutils.GetInferenceResultKey(borweinconsts.ModelNameBorweinTrainingThroughput), &borweintypes.BorweinInferenceResults{
		Timestamp: timeNow,
		Results: map[string]map[string][]*borweininfsvc.InferenceResult{
			"test": {
				"test": []*borweininfsvc.InferenceResult{
					{
						GenericOutput: string(bs),
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
		want    map[string]map[string]float64
		want1   int64
		wantErr bool
	}{
		{
			name:    "GetTrainingTHRegPredictValue failed",
			want:    nil,
			want1:   0,
			wantErr: true,
		},
		{
			name: "GetTrainingTHRegPredictValue failed",
			args: args{
				metaReader: reader,
			},
			want: map[string]map[string]float64{
				"test": {
					"test": 0.5,
				},
			},
			want1:   timeNow,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		curTT := tt
		t.Run(curTT.name, func(t *testing.T) {
			t.Parallel()
			got, got1, err := GetTrainingTHRegPredictValue(curTT.args.metaReader)
			if (err != nil) != curTT.wantErr {
				t.Errorf("GetTrainingTHRegPredictValue() error = %v, wantErr %v", err, curTT.wantErr)
				return
			}
			if !reflect.DeepEqual(got, curTT.want) {
				t.Errorf("GetTrainingTHRegPredictValue() got = %v, want %v", got, curTT.want)
			}
			if got1 != curTT.want1 {
				t.Errorf("GetTrainingTHRegPredictValue() got1 = %v, want %v", got1, curTT.want1)
			}
		})
	}
}
