package machine

import "sync"

type RDMATopologyProvider interface {
	GetRDMATopology() (*RDMATopology, bool, error)
	SetRDMATopology(*RDMATopology) error
}

type RDMATopology struct {
	RDMAs map[string]RDMAInfo
}

type RDMAInfo struct {
	Health    string
	NUMANodes []int
}

func (i RDMAInfo) GetNUMANode() []int {
	if i.NUMANodes == nil {
		return []int{}
	}
	return i.NUMANodes
}

type rdmaTopologyProviderImpl struct {
	mutex         sync.RWMutex
	resourceNames []string

	rdmaTopology      *RDMATopology
	numaTopologyReady bool
}

func NewRDMATopologyProvider(resourceNames []string) RDMATopologyProvider {

}
