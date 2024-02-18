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
	"time"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/api/core/v1"
	vpautil "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/util"

	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

const (
	// DefaultSampleWeight is the default weight of any sample (prior to including decaying factor)
	DefaultSampleWeight = float64(1)
	// DefaultMinSampleWeight is the minimal sample weight of any sample (prior to including decaying factor)
	DefaultMinSampleWeight = float64(0.1)
	// DefaultEpsilon is the minimal weight kept in histograms, it should be small enough that old samples
	// (just inside MemoryAggregationWindowLength) added with DefaultMinSampleWeight are still kept
	DefaultEpsilon = 0.001 * DefaultMinSampleWeight
	// DefaultHistogramBucketSizeGrowth is the default value for HistogramBucketSizeGrowth.
	DefaultHistogramBucketSizeGrowth = 0.05 // Make each bucket 5% larger than the previous one.

	// DefaultCPUHistogramMaxValue CPU histograms max bucket size is 1000.0 cores
	DefaultCPUHistogramMaxValue = 1000.0
	// DefaultCPUHistogramFirstBucketSize CPU histograms smallest bucket size is 0.01 cores
	DefaultCPUHistogramFirstBucketSize = 0.01

	// DefaultMemHistogramMaxValue Mem histograms max bucket size is 1TB
	DefaultMemHistogramMaxValue = 1e12
	// DefaultMemHistogramFirstBucketSize Mem histograms smallest bucket size is 10MB
	DefaultMemHistogramFirstBucketSize = 1e7

	DefaultCPUTaskProcessInterval = time.Minute * 10
	DefaultMemTaskProcessInterval = time.Hour * 1

	// DefaultHistogramDecayHalfLife is the default value for HistogramDecayHalfLife.
	DefaultHistogramDecayHalfLife = time.Hour * 24

	// DefaultInitDataLength is default data query span for the first run of the task
	DefaultInitDataLength = time.Hour * 25

	// MaxOfAllowNotExecutionTime is maximum non-run tolerance time for a task
	MaxOfAllowNotExecutionTime = 50 * time.Hour
)

func cpuHistogramOptions() vpautil.HistogramOptions {
	// CPU histograms use exponential bucketing scheme with the smallest bucket size of 0.01 core, max of 1000.0 cores
	options, err := vpautil.NewExponentialHistogramOptions(DefaultCPUHistogramMaxValue, DefaultCPUHistogramFirstBucketSize,
		1.+DefaultHistogramBucketSizeGrowth, DefaultEpsilon)
	if err != nil {
		panic("Invalid CPU histogram options") // Should not happen.
	}
	return options
}

func memoryHistogramOptions() vpautil.HistogramOptions {
	// Memory histograms use exponential bucketing scheme with the smallest bucket size of 10MB, max of 1TB
	options, err := vpautil.NewExponentialHistogramOptions(DefaultMemHistogramMaxValue, DefaultMemHistogramFirstBucketSize,
		1.+DefaultHistogramBucketSizeGrowth, DefaultEpsilon)
	if err != nil {
		panic("Invalid memory histogram options") // Should not happen.
	}
	return options
}

func HistogramOptionsFactory(resourceName v1.ResourceName) (vpautil.HistogramOptions, error) {
	switch resourceName {
	case v1.ResourceCPU:
		return cpuHistogramOptions(), nil
	case v1.ResourceMemory:
		return memoryHistogramOptions(), nil
	default:
		return nil, errors.Errorf("generate histogram options failed, unknow resource: %s", resourceName)
	}
}

func GetDefaultTaskProcessInterval(resourceName v1.ResourceName) (t time.Duration, err error) {
	switch resourceName {
	case v1.ResourceCPU:
		return DefaultCPUTaskProcessInterval, nil
	case v1.ResourceMemory:
		return DefaultMemTaskProcessInterval, nil
	default:
		return t, errors.Errorf("get task process interval failed, unknow resource: %s", resourceName)
	}
}

type ProcessConfig struct {
	DecayHalfLife time.Duration
}

const (
	ProcessConfigHalfLifeKey = "decayHalfLife"
)

func GetTaskConfig(extensions processortypes.TaskConfigStr) (*ProcessConfig, error) {
	processConfig := &ProcessConfig{
		DecayHalfLife: DefaultHistogramDecayHalfLife,
	}
	if extensions == "" {
		return processConfig, nil
	}
	config := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(extensions), config); err != nil {
		return nil, errors.Wrap(err, "unmarshal process config failed")
	}

	if value, ok := config[ProcessConfigHalfLifeKey]; ok {
		if halfLife, ok := value.(int); ok {
			processConfig.DecayHalfLife = time.Hour * time.Duration(halfLife)
		}
	}

	return processConfig, nil
}
