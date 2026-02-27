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

package userwatermarkdefault

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/userwatermark"
)

type DefaultOptions struct {
	EnableMemoryReclaim bool
	ReclaimInterval     int64

	ScaleFactor         uint64
	SingleReclaimFactor float64
	// SingleReclaimSize is the max memory reclaim size in one reclaim cycle
	SingleReclaimSize      uint64
	BackoffDuration        time.Duration
	FeedbackPolicy         string
	ReclaimFailedThreshold uint64
	FailureFreezePeriod    time.Duration

	PsiAvg60Threshold           float64
	ReclaimAccuracyTarget       float64
	ReclaimScanEfficiencyTarget float64
}

func NewDefaultOptions() *DefaultOptions {
	return &DefaultOptions{}
}

func (o *DefaultOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("user-watermark")

	fs.BoolVar(&o.EnableMemoryReclaim, "enable-user-watermark-memory-reclaim", o.EnableMemoryReclaim,
		"whether to enable memory reclaim")
	fs.Int64Var(&o.ReclaimInterval, "user-watermark-reclaim-interval", o.ReclaimInterval,
		"the interval to reclaim memory")
	fs.Uint64Var(&o.ScaleFactor, "user-watermark-scale-factor", o.ScaleFactor,
		"the scale factor to reclaim memory")
	fs.Uint64Var(&o.SingleReclaimSize, "user-watermark-single-reclaim-size", o.SingleReclaimSize,
		"the max memory reclaim size in one reclaim cycle")
	fs.Float64Var(&o.SingleReclaimFactor, "user-watermark-single-reclaim-factor", o.SingleReclaimFactor,
		"the factor to reclaim memory")
	fs.DurationVar(&o.BackoffDuration, "user-watermark-backoff-duration", o.BackoffDuration,
		"the duration to backoff after reclaim failed")
	fs.StringVar(&o.FeedbackPolicy, "user-watermark-feedback-policy", o.FeedbackPolicy,
		"the feedback policy to reclaim memory")
	fs.Uint64Var(&o.ReclaimFailedThreshold, "user-watermark-reclaim-failed-threshold", o.ReclaimFailedThreshold,
		"the threshold to trigger reclaim failed")
	fs.DurationVar(&o.FailureFreezePeriod, "user-watermark-failure-freeze-period", o.FailureFreezePeriod,
		"the period to freeze reclaim after trigger reclaim failed")
	fs.Float64Var(&o.PsiAvg60Threshold, "user-watermark-psi-avg60-threshold", o.PsiAvg60Threshold,
		"the threshold to trigger reclaim failed")
	fs.Float64Var(&o.ReclaimAccuracyTarget, "user-watermark-reclaim-accuracy-target", o.ReclaimAccuracyTarget,
		"the target reclaim accuracy")
	fs.Float64Var(&o.ReclaimScanEfficiencyTarget, "user-watermark-reclaim-scan-efficiency-target", o.ReclaimScanEfficiencyTarget,
		"the target reclaim scan efficiency")
}

func (o *DefaultOptions) ApplyTo(c *userwatermark.UserWatermarkDefaultConfiguration) error {
	c.EnableMemoryReclaim = o.EnableMemoryReclaim
	c.ReclaimInterval = o.ReclaimInterval

	c.ScaleFactor = o.ScaleFactor
	c.SingleReclaimSize = o.SingleReclaimSize
	c.SingleReclaimFactor = o.SingleReclaimFactor
	c.BackoffDuration = o.BackoffDuration
	c.FeedbackPolicy = v1alpha1.UserWatermarkPolicyName(o.FeedbackPolicy)
	c.ReclaimFailedThreshold = o.ReclaimFailedThreshold
	c.FailureFreezePeriod = o.FailureFreezePeriod
	c.PsiAvg60Threshold = o.PsiAvg60Threshold
	c.ReclaimAccuracyTarget = o.ReclaimAccuracyTarget
	c.ReclaimScanEfficiencyTarget = o.ReclaimScanEfficiencyTarget
	return nil
}
