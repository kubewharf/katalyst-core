package mbsourcing

// feedbackSourcer takes into considerations of previous {desired-outgoing-quota, observed-outgoing-traffic}
// to adaptively adjust desired-outgoing-quota in order to get desired-outgoing-target
type feedbackSourcer struct {
	innerSourcer          Sourcer
	previousOutgoingQuota []int
}

func (f *feedbackSourcer) AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int {
	desiredOutgoingTarget := f.innerSourcer.AttributeIncomingMBToSources(domainTargets)
	observedOutgoingMB := []int{
		domainTargets[0].MBSource,
		domainTargets[1].MBSource,
	}
	result := f.getByFeedback(observedOutgoingMB, desiredOutgoingTarget)
	f.previousOutgoingQuota = result
	return result
}

func (f *feedbackSourcer) getByFeedback(observedOutgoingMB []int, desiredOutgoingMB []int) []int {
	if f.previousOutgoingQuota == nil {
		return desiredOutgoingMB
	}

	result := make([]int, len(desiredOutgoingMB))
	for domain := range result {
		result[domain] = getByFeedback(f.previousOutgoingQuota[domain],
			observedOutgoingMB[domain],
			desiredOutgoingMB[domain])
	}
	return result
}

func getByFeedback(previous, observed, desired int) int {
	// previous ==> observed-outcome
	// ?        ==> desired-outcome
	if observed == 0 || previous == 0 {
		// not sure how to getting out of the previous x  observed; keep going
		return desired
	}
	return int(float64(desired) * float64(previous) / float64(observed))
}

func newFeedbackSourcer(sourcer Sourcer) Sourcer {
	return &feedbackSourcer{
		innerSourcer: sourcer,
	}
}
