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
	"testing"

	"github.com/stretchr/testify/assert"
	cliflag "k8s.io/component-base/cli/flag"

	v1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	dynamicuserwm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/userwatermark"
)

func TestNewDefaultOptions(t *testing.T) {
	t.Parallel()

	opts := NewDefaultOptions()
	assert.NotNil(t, opts)
}

func TestDefaultOptions_AddFlags(t *testing.T) {
	t.Parallel()

	opts := NewDefaultOptions()
	fss := &cliflag.NamedFlagSets{}

	opts.AddFlags(fss)

	fs := fss.FlagSet("user-watermark")

	assert.NotNil(t, fs.Lookup("enable-user-watermark-memory-reclaim"))
	assert.NotNil(t, fs.Lookup("user-watermark-reclaim-interval"))
	assert.NotNil(t, fs.Lookup("user-watermark-scale-factor"))
	assert.NotNil(t, fs.Lookup("user-watermark-single-reclaim-size"))
	assert.NotNil(t, fs.Lookup("user-watermark-single-reclaim-factor"))
	assert.NotNil(t, fs.Lookup("user-watermark-backoff-duration"))

	assert.NotNil(t, fs.Lookup("user-watermark-feedback-policy"))
	assert.NotNil(t, fs.Lookup("user-watermark-reclaim-failed-threshold"))
	assert.NotNil(t, fs.Lookup("user-watermark-failure-freeze-period"))
	assert.NotNil(t, fs.Lookup("user-watermark-psi-avg60-threshold"))
	assert.NotNil(t, fs.Lookup("user-watermark-reclaim-accuracy-target"))
	assert.NotNil(t, fs.Lookup("user-watermark-reclaim-scan-efficiency-target"))
}

func TestDefaultOptions_ApplyTo(t *testing.T) {
	t.Parallel()

	opts := &DefaultOptions{
		EnableMemoryReclaim: true,
		ReclaimInterval:     5,
		ScaleFactor:         200,
		SingleReclaimFactor: 0.5,
		SingleReclaimSize:   1 << 20,
		BackoffDuration:     10,
		FeedbackPolicy:      string(v1alpha1.UserWatermarkPolicyNamePSI),

		ReclaimFailedThreshold:      3,
		FailureFreezePeriod:         20,
		PsiAvg60Threshold:           1.0,
		ReclaimAccuracyTarget:       0.8,
		ReclaimScanEfficiencyTarget: 0.5,
	}

	conf := &dynamicuserwm.UserWatermarkDefaultConfiguration{}
	err := opts.ApplyTo(conf)

	assert.NoError(t, err)
	assert.True(t, conf.EnableMemoryReclaim)
	assert.Equal(t, int64(5), conf.ReclaimInterval)
	assert.Equal(t, uint64(200), conf.ScaleFactor)
	assert.Equal(t, 0.5, conf.SingleReclaimFactor)
	assert.Equal(t, uint64(1<<20), conf.SingleReclaimSize)
	assert.Equal(t, opts.BackoffDuration, conf.BackoffDuration)
	assert.Equal(t, v1alpha1.UserWatermarkPolicyNamePSI, conf.FeedbackPolicy)
	assert.Equal(t, uint64(3), conf.ReclaimFailedThreshold)
	assert.Equal(t, opts.FailureFreezePeriod, conf.FailureFreezePeriod)
	assert.Equal(t, opts.PsiAvg60Threshold, conf.PsiAvg60Threshold)
	assert.Equal(t, opts.ReclaimAccuracyTarget, conf.ReclaimAccuracyTarget)
	assert.Equal(t, opts.ReclaimScanEfficiencyTarget, conf.ReclaimScanEfficiencyTarget)
}
