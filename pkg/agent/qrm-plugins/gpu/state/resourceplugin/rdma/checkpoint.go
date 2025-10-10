package rdma

import (
	"encoding/json"

	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
)

var _ checkpointmanager.Checkpoint = &RDMAPluginCheckpoint{}

type RDMAPluginCheckpoint struct {
	PolicyName   string            `json:"policyName"`
	MachineState RDMAMap           `json:"machineState"`
	PodEntries   PodEntries        `json:"pod_entries"`
	Checksum     checksum.Checksum `json:"checksum"`
}

func NewRDMAPluginCheckpoint() *RDMAPluginCheckpoint {
	return &RDMAPluginCheckpoint{
		PodEntries:   make(PodEntries),
		MachineState: make(RDMAMap),
	}
}

// MarshalCheckpoint returns a marshaled checkpoint
func (cp *RDMAPluginCheckpoint) MarshalCheckpoint() ([]byte, error) {
	// make sure checksum wasn't set before, so it doesn't affect output checksum
	cp.Checksum = 0
	cp.Checksum = checksum.New(cp)
	return json.Marshal(*cp)
}

// UnmarshalCheckpoint tries to unmarshal passed bytes to checkpoint
func (cp *RDMAPluginCheckpoint) UnmarshalCheckpoint(blob []byte) error {
	return json.Unmarshal(blob, cp)
}

// VerifyChecksum verifies that current checksum of checkpoint is valid
func (cp *RDMAPluginCheckpoint) VerifyChecksum() error {
	ck := cp.Checksum
	cp.Checksum = 0
	err := ck.Verify(cp)
	cp.Checksum = ck
	return err
}
