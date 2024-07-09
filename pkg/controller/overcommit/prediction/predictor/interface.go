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

package predictor

import (
	"context"
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
)

type Interface interface {
	// PredictTimeSeries predict time series data by time series history metrics
	PredictTimeSeries(ctx context.Context, args *common.PredictArgs, history *common.TimeSeries) (*common.TimeSeries, error)
}

type FakePredictor struct {
	ts *common.TimeSeries
}

func NewFakePredictor(timeSeries *common.TimeSeries) *FakePredictor {
	return &FakePredictor{
		ts: timeSeries,
	}
}

func (f *FakePredictor) PredictTimeSeries(ctx context.Context, args *common.PredictArgs, history *common.TimeSeries) (*common.TimeSeries, error) {
	if f.ts == nil {
		return nil, fmt.Errorf("test error")
	}

	return f.ts, nil
}
