package mbsourcing

import (
	"math"

	policyconfig "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"
)

// feedbackSourcer takes into considerations of previous {desired-outgoing-quota, observed-outgoing-traffic}
// to adaptively adjust desired-outgoing-quota in order to get desired-outgoing-target
type feedbackSourcer struct {
	innerSourcer          Sourcer
	previousOutgoingQuota []int
}

func (f *feedbackSourcer) updatePreviousOutgoingQuota(data []int) {
	f.previousOutgoingQuota = data
}

func (f *feedbackSourcer) getPreviousOutgoingQuota() []int {
	return f.previousOutgoingQuota
}

func (f *feedbackSourcer) AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int {
	desiredOutgoingTarget := f.innerSourcer.AttributeIncomingMBToSources(domainTargets)
	observedOutgoingMB := []int{
		domainTargets[0].MBSource,
		domainTargets[1].MBSource,
	}
	result := f.getByFeedback(observedOutgoingMB, desiredOutgoingTarget)
	f.updatePreviousOutgoingQuota(result)
	return result
}

func (f *feedbackSourcer) getByFeedback(observedOutgoingMB []int, desiredOutgoingMB []int) []int {
	prev := f.getPreviousOutgoingQuota()
	if prev == nil || len(prev) != 2 {
		return desiredOutgoingMB
	}

	result := make([]int, len(desiredOutgoingMB))
	for domain := range result {
		result[domain] = getByFeedback(prev[domain],
			observedOutgoingMB[domain],
			desiredOutgoingMB[domain])
	}
	return result
}

func getByFeedback(previous, observed, desired int) int {
	// previous ==> observed-outcome
	// ?        ==> desired-outcome
	if observed == 0 || previous <= 0 {
		// not sure how to get out of the previous x  observed; keep going
		return desired
	}
	result := int(float64(desired) * float64(previous) / float64(observed))
	if result >= math.MaxInt || result <= math.MinInt {
		result = policyconfig.PolicyConfig.DomainMBMax
	}
	return result
}

func newFeedbackSourcer(sourcer Sourcer) Sourcer {
	return &feedbackSourcer{
		innerSourcer: sourcer,
	}
}
