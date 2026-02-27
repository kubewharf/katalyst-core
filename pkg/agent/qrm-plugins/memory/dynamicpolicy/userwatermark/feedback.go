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
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/userwatermark"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func init() {
	RegisterFeedbackPolicy(v1alpha1.UserWatermarkPolicyNamePSI, memoryPSIPolicy)
	RegisterFeedbackPolicy(v1alpha1.UserWatermarkPolicyNameRefault, refaultPolicy)
	RegisterFeedbackPolicy(v1alpha1.UserWatermarkPolicyNameIntegrated, integratedPolicy)
}

var feedbackPolicies sync.Map

func RegisterFeedbackPolicy(name v1alpha1.UserWatermarkPolicyName, plugin FeedbackPolicy) {
	feedbackPolicies.Store(name, plugin)
}

func GetRegisteredFeedbackPolices() map[v1alpha1.UserWatermarkPolicyName]FeedbackPolicy {
	res := make(map[v1alpha1.UserWatermarkPolicyName]FeedbackPolicy)
	feedbackPolicies.Range(func(key, value interface{}) bool {
		res[key.(v1alpha1.UserWatermarkPolicyName)] = value.(FeedbackPolicy)
		return true
	})
	return res
}

type FeedbackResult struct {
	Abnormal bool
	Reason   string
}

type FeedbackManager struct {
	policyName       v1alpha1.UserWatermarkPolicyName
	feedbackPolicies map[v1alpha1.UserWatermarkPolicyName]FeedbackPolicy
}
type FeedbackPolicy func(lastStats ReclaimStats, currStats ReclaimStats, conf *userwatermark.ReclaimConfigDetail, emitter metrics.MetricEmitter) FeedbackResult

func NewFeedbackManager() *FeedbackManager {
	return &FeedbackManager{
		policyName:       v1alpha1.UserWatermarkPolicyNameIntegrated,
		feedbackPolicies: GetRegisteredFeedbackPolices(),
	}
}

func (f *FeedbackManager) UpdateFeedbackPolicy(policyName v1alpha1.UserWatermarkPolicyName) {
	f.policyName = policyName
	general.InfofV(5, "Update feedback policy success, policyName:%v", f.policyName)
}

func (f *FeedbackManager) FeedbackResult(lastStats ReclaimStats, currStats ReclaimStats, conf *userwatermark.ReclaimConfigDetail, emitter metrics.MetricEmitter) (FeedbackResult, error) {
	switch f.policyName {
	case v1alpha1.UserWatermarkPolicyNamePSI:
		return memoryPSIPolicy(lastStats, currStats, conf, emitter), nil
	case v1alpha1.UserWatermarkPolicyNameRefault:
		return refaultPolicy(lastStats, currStats, conf, emitter), nil
	case v1alpha1.UserWatermarkPolicyNameIntegrated:
		return integratedPolicy(lastStats, currStats, conf, emitter), nil
	default:
		return FeedbackResult{}, fmt.Errorf("feedback policy %v not registered", f.policyName)
	}
}

func integratedPolicy(lastStats ReclaimStats, currStats ReclaimStats, conf *userwatermark.ReclaimConfigDetail, emitter metrics.MetricEmitter) FeedbackResult {
	result := memoryPSIPolicy(lastStats, currStats, conf, emitter)
	if result.Abnormal {
		return result
	}

	result = refaultPolicy(lastStats, currStats, conf, emitter)
	if result.Abnormal {
		return result
	}

	return result
}

func memoryPSIPolicy(lastStats ReclaimStats, currStats ReclaimStats, conf *userwatermark.ReclaimConfigDetail, emitter metrics.MetricEmitter) FeedbackResult {
	result := FeedbackResult{}
	if currStats.memPsiAvg60 >= conf.PsiAvg60Threshold {
		result.Abnormal = true
		result.Reason = fmt.Sprintf("memPsiAvg60 %.4f >= threshold %.4f", currStats.memPsiAvg60, conf.PsiAvg60Threshold)
	}

	return result
}

func refaultPolicy(lastStats ReclaimStats, currStats ReclaimStats, conf *userwatermark.ReclaimConfigDetail, emitter metrics.MetricEmitter) FeedbackResult {
	result := FeedbackResult{}

	reclaimAccuracyRatio := getReclaimAccuracyRatio(lastStats, currStats)
	reclaimScanEfficiencyRatio := getReclaimScanEfficiencyRatio(lastStats, currStats)

	if reclaimAccuracyRatio < conf.RefaultPolicyConf.ReclaimAccuracyTarget || reclaimScanEfficiencyRatio < conf.RefaultPolicyConf.ReclaimScanEfficiencyTarget {
		result.Abnormal = true
		result.Reason = fmt.Sprintf("reclaimAccuracyRatio %.4f < target %.4f || reclaimScanEfficiencyRatio %.4f < target %.4f",
			reclaimAccuracyRatio, conf.RefaultPolicyConf.ReclaimAccuracyTarget, reclaimScanEfficiencyRatio, conf.RefaultPolicyConf.ReclaimScanEfficiencyTarget)
	}

	return result
}

func getReclaimAccuracyRatio(lastStats ReclaimStats, currStats ReclaimStats) float64 {
	pgstealDelta := currStats.pgsteal - lastStats.pgsteal
	refaultDelta := currStats.refaultActivate - lastStats.refaultActivate
	reclaimAccuracyRatio := 1.0
	if pgstealDelta > 0 {
		reclaimAccuracyRatio = 1 - refaultDelta/pgstealDelta
	}

	return reclaimAccuracyRatio
}

func getReclaimScanEfficiencyRatio(lastStats ReclaimStats, currStats ReclaimStats) float64 {
	pgscanDelta := currStats.pgscan - lastStats.pgscan
	pgstealDelta := currStats.pgsteal - lastStats.pgsteal
	reclaimScanEfficiencyRatio := 1.0
	if pgscanDelta > 0 && pgstealDelta > 0 {
		reclaimScanEfficiencyRatio = pgstealDelta / pgscanDelta
	}

	return reclaimScanEfficiencyRatio
}
