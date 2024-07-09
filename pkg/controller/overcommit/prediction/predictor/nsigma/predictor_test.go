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

package nsigma

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
)

func TestPredictTimeSeries(t *testing.T) {
	t.Parallel()

	history := generateTestTimeSeries()

	p := NewPredictor(3, 24)
	predictTs, err := p.PredictTimeSeries(context.TODO(), &common.PredictArgs{ResourceName: "cpu"}, history)
	assert.NoError(t, err)
	assert.Equal(t, len(predictTs.Samples), 24)

	predictTs2, err := p.PredictTimeSeries(context.TODO(), &common.PredictArgs{ResourceName: "cpu"}, generateRandTimeSeries())
	assert.NoError(t, err)
	assert.Equal(t, len(predictTs2.Samples), 24)

	p = NewPredictor(3, 1)
	predictTs3, err := p.PredictTimeSeries(context.TODO(), &common.PredictArgs{ResourceName: "cpu"}, history)
	assert.NoError(t, err)
	assert.Equal(t, len(predictTs3.Samples), 1)
}

func generateTestTimeSeries() *common.TimeSeries {
	rand.Seed(time.Now().UnixNano())
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	nextDay := day.Add(24 * time.Hour)
	mean := 2.0
	stddev := 0.5

	res := common.EmptyTimeSeries()
	for day.Before(nextDay) {
		res.Samples = append(res.Samples, common.Sample{
			Value:     rand.NormFloat64()*stddev + mean,
			Timestamp: day.Unix(),
		})

		day = day.Add(1 * time.Minute)
	}

	return res
}

func generateRandTimeSeries() *common.TimeSeries {
	rand.Seed(time.Now().UnixNano())
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	nextDay := day.Add(24 * time.Hour)

	res := common.EmptyTimeSeries()
	for day.Before(nextDay) {
		res.Samples = append(res.Samples, common.Sample{
			Value:     rand.Float64() * 4,
			Timestamp: day.Unix(),
		})

		day = day.Add(1 * time.Minute)
	}

	return res
}
