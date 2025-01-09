package mbsourcing

// getLocalRatio gets the ratio of local mb out of total for the given mb domain
// local mb is the mb amount accessing the memory in the domain (e.g. cpu socket);
// remote mb is amount accessing memory to other domain (e.g. the other cpu socket).
// both local and remote mb is originated from same domain, but the destination is different.
func getLocalRatio(data DomainMBTargetSource) float64 {
	if data.MBSource == 0 {
		return 1.0
	}
	if data.MBSourceRemote >= data.MBSource {
		return 0.0
	}
	return 1 - float64(data.MBSourceRemote)/float64(data.MBSource)
}
