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

package userwatermark

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	dynamicuserwm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/userwatermark"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestNewFeedbackManager_DefaultPolicyAndRegistry(t *testing.T) {
	t.Parallel()

	mgr := NewFeedbackManager()
	assert.Equal(t, v1alpha1.UserWatermarkPolicyNameIntegrated, mgr.policyName)

	policies := mgr.feedbackPolicies

	// basic sanity: three builtin policies should be registered
	_, hasPSI := policies[v1alpha1.UserWatermarkPolicyNamePSI]
	_, hasRefault := policies[v1alpha1.UserWatermarkPolicyNameRefault]
	_, hasIntegrated := policies[v1alpha1.UserWatermarkPolicyNameIntegrated]

	assert.True(t, hasPSI)
	assert.True(t, hasRefault)
	assert.True(t, hasIntegrated)
}

func TestFeedbackManager_UpdateAndDispatch(t *testing.T) {
	t.Parallel()

	mgr := NewFeedbackManager()

	conf := dynamicuserwm.NewReclaimConfigDetail(dynamicuserwm.NewUserWatermarkDefaultConfiguration())
	conf.PsiAvg60Threshold = 1.0
	conf.RefaultPolicyConf.ReclaimAccuracyTarget = 0.0
	conf.RefaultPolicyConf.ReclaimScanEfficiencyTarget = 0.0
	emitter := metrics.DummyMetrics{}

	last := ReclaimStats{}
	curr := ReclaimStats{}

	// PSI policy
	mgr.UpdateFeedbackPolicy(v1alpha1.UserWatermarkPolicyNamePSI)
	res, err := mgr.FeedbackResult(last, curr, conf, emitter)
	assert.NoError(t, err)
	assert.False(t, res.Abnormal)

	// Refault policy
	mgr.UpdateFeedbackPolicy(v1alpha1.UserWatermarkPolicyNameRefault)
	res, err = mgr.FeedbackResult(last, curr, conf, emitter)
	assert.NoError(t, err)
	assert.False(t, res.Abnormal)

	// Integrated policy
	mgr.UpdateFeedbackPolicy(v1alpha1.UserWatermarkPolicyNameIntegrated)
	res, err = mgr.FeedbackResult(last, curr, conf, emitter)
	assert.NoError(t, err)
	assert.False(t, res.Abnormal)

	// Unknown policy should return error
	mgr.UpdateFeedbackPolicy("unknown-policy")
	_, err = mgr.FeedbackResult(last, curr, conf, emitter)
	assert.Error(t, err)
}

func TestIntegratedPolicy_Order(t *testing.T) {
	t.Parallel()

	conf := dynamicuserwm.NewReclaimConfigDetail(dynamicuserwm.NewUserWatermarkDefaultConfiguration())

	emitter := metrics.DummyMetrics{}
	last := ReclaimStats{}
	curr := ReclaimStats{}

	// when PSI policy reports abnormal, integrated should stop early
	conf.PsiAvg60Threshold = 0.5
	curr.memPsiAvg60 = 1.0

	res := integratedPolicy(last, curr, conf, emitter)
	assert.True(t, res.Abnormal)
	assert.Contains(t, res.Reason, "memPsiAvg60")

	// when PSI is normal but refault policy is abnormal
	curr.memPsiAvg60 = 0.0
	conf.RefaultPolicyConf = &dynamicuserwm.RefaultPolicyConf{
		ReclaimAccuracyTarget:       0.8,
		ReclaimScanEfficiencyTarget: 0.5,
	}
	last = ReclaimStats{pgsteal: 100, pgscan: 200}
	curr = ReclaimStats{pgsteal: 200, pgscan: 400, refaultActivate: 90}

	res = integratedPolicy(last, curr, conf, emitter)
	assert.True(t, res.Abnormal)
	assert.Contains(t, res.Reason, "reclaimAccuracyRatio")
}

func TestMemoryPSIPolicy(t *testing.T) {
	t.Parallel()

	conf := dynamicuserwm.NewReclaimConfigDetail(dynamicuserwm.NewUserWatermarkDefaultConfiguration())
	emitter := metrics.DummyMetrics{}

	last := ReclaimStats{}
	curr := ReclaimStats{}
	conf.PsiAvg60Threshold = 1.0

	// below threshold
	curr.memPsiAvg60 = 0.5
	res := memoryPSIPolicy(last, curr, conf, emitter)
	assert.False(t, res.Abnormal)

	// equal to threshold should be abnormal
	curr.memPsiAvg60 = 1.0
	res = memoryPSIPolicy(last, curr, conf, emitter)
	assert.True(t, res.Abnormal)
	assert.Contains(t, res.Reason, "memPsiAvg60")
}

func TestRefaultPolicy(t *testing.T) {
	t.Parallel()

	conf := dynamicuserwm.NewReclaimConfigDetail(dynamicuserwm.NewUserWatermarkDefaultConfiguration())
	conf.RefaultPolicyConf = &dynamicuserwm.RefaultPolicyConf{
		ReclaimAccuracyTarget:       0.8,
		ReclaimScanEfficiencyTarget: 0.5,
	}
	emitter := metrics.DummyMetrics{}

	last := ReclaimStats{pgsteal: 100, pgscan: 200}
	curr := ReclaimStats{pgsteal: 200, pgscan: 400, refaultActivate: 10}

	// good accuracy and efficiency -> normal
	res := refaultPolicy(last, curr, conf, emitter)
	assert.False(t, res.Abnormal)

	// bad accuracy -> abnormal
	curr.refaultActivate = 90
	res = refaultPolicy(last, curr, conf, emitter)
	assert.True(t, res.Abnormal)
	assert.Contains(t, res.Reason, "reclaimAccuracyRatio")
}

func TestGetReclaimAccuracyAndScanEfficiencyRatio(t *testing.T) {
	t.Parallel()

	last := ReclaimStats{pgsteal: 100, pgscan: 200}
	curr := ReclaimStats{pgsteal: 200, pgscan: 400, refaultActivate: 50}

	accuracy := getReclaimAccuracyRatio(last, curr)
	scanEff := getReclaimScanEfficiencyRatio(last, curr)

	// pgstealDelta=100, refaultDelta=50 => 1 - 50/100 = 0.5
	assert.InDelta(t, 0.5, accuracy, 1e-6)
	// pgscanDelta=200, pgstealDelta=100 => 100/200 = 0.5
	assert.InDelta(t, 0.5, scanEff, 1e-6)
	// guard branches when denominators are zero
	accuracy = getReclaimAccuracyRatio(ReclaimStats{}, ReclaimStats{})
	assert.InDelta(t, 1.0, accuracy, 1e-6)

	scanEff = getReclaimScanEfficiencyRatio(ReclaimStats{}, ReclaimStats{})
	assert.InDelta(t, 1.0, scanEff, 1e-6)
}
