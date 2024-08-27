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

package task

import (
	"context"
	"fmt"
	"math"
	"runtime/debug"
	"sync"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	vpautil "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/util"

	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	"github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/log"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

var (
	DataPreparingErr         = errors.New("data preparing")
	SampleExpirationErr      = errors.New("no samples in the last 24 hours")
	InsufficientSampleErr    = errors.New("The data sample is insufficient to obtain the predicted value")
	QuerySamplesIsEmptyErr   = errors.New("the sample found is empty")
	HistogramTaskRunPanicErr = errors.New("histogram task run panic")
)

type HistogramTask struct {
	mutex sync.Mutex

	histogram         vpautil.Histogram
	decayHalfLife     time.Duration
	firstSampleTime   time.Time
	lastSampleTime    time.Time
	totalSamplesCount int

	createTime  time.Time
	lastRunTime time.Time

	metric          datasourcetypes.Metric
	ProcessInterval time.Duration
}

func NewTask(metric datasourcetypes.Metric, config processortypes.TaskConfigStr) (*HistogramTask, error) {
	processConfig, err := GetTaskConfig(config)
	if err != nil {
		return nil, errors.Wrapf(err, "New histogram task error, get process config failed, config: %s", config)
	}
	histogramOptions, err := HistogramOptionsFactory(metric.Resource)
	if err != nil {
		return nil, errors.Wrap(err, "get histogram options failed")
	}
	taskProcessInterval, err := GetDefaultTaskProcessInterval(metric.Resource)
	if err != nil {
		return nil, errors.Wrap(err, "get process interval failed")
	}
	return &HistogramTask{
		mutex:           sync.Mutex{},
		metric:          metric,
		histogram:       vpautil.NewDecayingHistogram(histogramOptions, processConfig.DecayHalfLife),
		decayHalfLife:   processConfig.DecayHalfLife,
		createTime:      time.Now(),
		ProcessInterval: taskProcessInterval,
	}, nil
}

func (t *HistogramTask) AddSample(sampleTime time.Time, sampleValue float64, sampleWeight float64) {
	t.histogram.AddSample(sampleValue, sampleWeight, sampleTime)
	if t.lastSampleTime.Before(sampleTime) {
		t.lastSampleTime = sampleTime
	}
	if t.firstSampleTime.IsZero() || t.firstSampleTime.After(sampleTime) {
		t.firstSampleTime = sampleTime
	}
	t.totalSamplesCount++
}

func (t *HistogramTask) AddRangeSample(firstSampleTime, lastSampleTime time.Time, sampleValue float64, sampleWeight float64) {
	t.histogram.AddSample(sampleValue, sampleWeight, lastSampleTime)
	if t.lastSampleTime.Before(lastSampleTime) {
		t.lastSampleTime = lastSampleTime
	}
	if t.firstSampleTime.IsZero() || t.firstSampleTime.After(firstSampleTime) {
		t.firstSampleTime = firstSampleTime
	}
	t.totalSamplesCount++
}

func (t *HistogramTask) Run(ctx context.Context, datasourceProxy *datasource.Proxy) (nextRunInterval time.Duration, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.ErrorS(ctx, HistogramTaskRunPanicErr, fmt.Sprintf("%v", r), "stack", string(debug.Stack()))
			err = HistogramTaskRunPanicErr
		}
	}()

	t.mutex.Lock()
	defer t.mutex.Unlock()

	log.InfoS(ctx, "percentile process task run")

	runSectionBegin := t.lastRunTime
	runSectionEnd := time.Now()
	t.lastRunTime = runSectionEnd
	if runSectionBegin.IsZero() {
		runSectionBegin = runSectionEnd.Add(-DefaultInitDataLength)
	}
	ctx = log.SetKeysAndValues(ctx, "runSectionBegin", runSectionBegin.String(), "runSectionEnd", runSectionEnd.String())

	timeSeries, err := datasourceProxy.QueryTimeSeries(datasource.PrometheusDatasource, t.metric, runSectionBegin, runSectionEnd, time.Minute)
	if err != nil {
		log.ErrorS(ctx, err, "task handler error, query samples failed")
		return 0, err
	}
	samplesOverview := datasourcetypes.GetSamplesOverview(timeSeries)
	if samplesOverview == nil {
		log.ErrorS(ctx, QuerySamplesIsEmptyErr, "No sample found, histogram task run termination")
		return 0, QuerySamplesIsEmptyErr
	}
	log.InfoS(ctx, "percentile process task query samples", "samplesOverview", samplesOverview)

	switch t.metric.Resource {
	case v1.ResourceMemory:
		lastSampleTime := time.Unix(samplesOverview.LastTimestamp, 0)
		firstSampleTime := time.Unix(samplesOverview.FirstTimestamp, 0)
		t.AddRangeSample(firstSampleTime, lastSampleTime, samplesOverview.MaxValue, DefaultSampleWeight)
		log.V(5).InfoS(ctx, "Mem peak sample added.",
			"firstSampleTime", firstSampleTime, "lastSampleTime", lastSampleTime,
			"SampleValue", samplesOverview.MaxValue, "SampleNum", len(timeSeries.Samples))
	case v1.ResourceCPU:
		for _, sample := range timeSeries.Samples {
			sampleTime := time.Unix(sample.Timestamp, 0)
			sampleWeight := math.Max(sample.Value, DefaultMinSampleWeight)
			t.AddSample(sampleTime, sample.Value, sampleWeight)
			log.V(5).InfoS(ctx, "CPU sample added",
				"sampleTime", sampleTime, "sampleWeight", sampleWeight, "SampleValue", sample.Value)
		}
	}

	log.InfoS(ctx, "percentile process task run finished")
	return t.ProcessInterval, nil
}

func (t *HistogramTask) QueryPercentileValue(ctx context.Context, percentile float64) (float64, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.firstSampleTime.IsZero() {
		return 0, DataPreparingErr
	}
	if time.Now().Sub(t.lastSampleTime) > 24*time.Hour {
		return 0, SampleExpirationErr
	}
	if t.lastSampleTime.Sub(t.firstSampleTime) < time.Hour*24 {
		return 0, InsufficientSampleErr
	}
	// TODO:  Check whether the ratio between the count of samples and the running time meets the requirements

	percentileValue := t.histogram.Percentile(percentile)

	log.InfoS(ctx, "Query Processed Values",
		"lastSampleTime", t.lastSampleTime, "firstSampleTime", t.firstSampleTime,
		"totalSamplesCount", t.totalSamplesCount, "percentileValue", percentileValue)
	return percentileValue, nil
}

func (t *HistogramTask) IsTimeoutNotExecute() bool {
	baseTime := t.createTime
	if !t.lastRunTime.IsZero() {
		baseTime = t.lastRunTime
	}
	// If there is no execution record within 10 task execution intervals, the task is regarded as invalid
	return baseTime.Add(MaxOfAllowNotExecutionTime).Before(time.Now())
}
