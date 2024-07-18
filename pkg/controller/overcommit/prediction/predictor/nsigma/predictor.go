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
	"math"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/predictor"
)

const NSigmaPredictor = "NSigma"

type Predictor struct {
	N       int
	Buckets int
}

func NewPredictor(factor int, buckets int) predictor.Interface {
	return &Predictor{
		N:       factor,
		Buckets: buckets,
	}
}

func (p *Predictor) PredictTimeSeries(_ context.Context, args *common.PredictArgs, history *common.TimeSeries) (*common.TimeSeries, error) {
	historyBuckets := make([][]common.Sample, p.Buckets)
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		klog.Errorf("load location fail: %v", err)
		location = time.Now().Location()
	}

	for i := range history.Samples {
		hour := time.Unix(history.Samples[i].Timestamp/1000, 0).In(location).Hour()
		if historyBuckets[hour%p.Buckets] == nil {
			historyBuckets[hour%p.Buckets] = make([]common.Sample, 0)
		}
		historyBuckets[hour%p.Buckets] = append(historyBuckets[hour%p.Buckets], history.Samples[i])
	}

	res := common.EmptyTimeSeries()
	for i := range history.Metadata {
		res.Metadata = append(res.Metadata, history.Metadata[i])
	}

	// use next day hours as timestamp
	nextDay := truncateToNextDay(location)
	for i := range historyBuckets {
		peak := p.predictPeak(historyBuckets[i])
		if args.ResourceName == v1.ResourceCPU.String() {
			peak = peak * 1000
		}
		res.Samples = append(res.Samples, common.Sample{
			Value:     peak,
			Timestamp: nextDay.Add(time.Duration(i) * time.Hour).Unix(),
		})
	}

	return res, nil
}

func (p *Predictor) predictPeak(samples []common.Sample) float64 {
	m := mean(samples)
	std := stdev(samples, m)

	return m + float64(p.N)*std
}

func mean(samples []common.Sample) float64 {
	l := len(samples)
	if l <= 0 {
		return 0
	}

	sum := 0.0
	for i := range samples {
		sum += samples[i].Value
	}

	return sum / float64(l)
}

func stdev(samples []common.Sample, mean float64) float64 {
	l := len(samples)
	if l <= 0 {
		return 0
	}

	sd := 0.0
	for i := range samples {
		sd += math.Pow(samples[i].Value-mean, 2)
	}
	sd = math.Sqrt(sd / float64(l))
	return sd
}

func truncateToNextDay(location *time.Location) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location).Add(24 * time.Hour)
}
