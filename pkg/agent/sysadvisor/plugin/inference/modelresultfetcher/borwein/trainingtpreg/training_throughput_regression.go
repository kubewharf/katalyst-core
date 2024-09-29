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
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	borweinutils "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/utils"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type TrainingThroughputRegression struct {
	PredictValue float64 `json:"predict_value"`
}

func GetTrainingTHRegPredictValue(metaReader metacache.MetaReader) (map[string]map[string]float64, int64, error) {
	if metaReader == nil {
		return nil, 0, fmt.Errorf("nil metaReader")
	}

	inferenceResultKey := borweinutils.GetInferenceResultKey(borweinconsts.ModelNameBorweinTrainingThroughput)
	results, err := metaReader.GetInferenceResult(inferenceResultKey)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get inference results for %s", inferenceResultKey)
	}

	ret := make(map[string]map[string]float64)
	var resultTimestamp int64
	switch typedResults := results.(type) {
	case *borweintypes.BorweinInferenceResults:
		resultTimestamp = typedResults.Timestamp
		typedResults.RangeInferenceResults(func(podUID, containerName string, result *borweininfsvc.InferenceResult) {
			if result == nil {
				return
			}

			specificResult := &TrainingThroughputRegression{}
			err := json.Unmarshal([]byte(result.GenericOutput), specificResult)
			if err != nil {
				general.Errorf("invalid generic output: %s for %s", result.GenericOutput, inferenceResultKey)
				return
			}

			if ret[podUID] == nil {
				ret[podUID] = make(map[string]float64)
			}

			ret[podUID][containerName] = specificResult.PredictValue
		})
	default:
		return nil, 0, fmt.Errorf("invalid model result type: %T", typedResults)
	}

	return ret, resultTimestamp, nil
}
