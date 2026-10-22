/*
Copyright 2026 The Katalyst Authors.

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

package state

import (
	"encoding/json"
	"maps"
	"slices"

	resourcev1 "k8s.io/api/resource/v1"
)

type ClaimPhase string

const (
	PrepareStarted   ClaimPhase = "PrepareStarted"
	PrepareCompleted ClaimPhase = "PrepareCompleted"
)

type PreparedDevice struct {
	Requests     []string `json:"requests"`
	PoolName     string   `json:"poolName"`
	DeviceName   string   `json:"deviceName"`
	CDIDeviceIDs []string `json:"cdiDeviceIDs,omitempty"`
}

type PreparedClaim struct {
	UID           string                         `json:"uid"`
	Namespace     string                         `json:"namespace"`
	Name          string                         `json:"name"`
	Labels        map[string]string              `json:"labels,omitempty"`
	Status        resourcev1.ResourceClaimStatus `json:"status"`
	Phase         ClaimPhase                     `json:"phase"`
	Devices       []PreparedDevice               `json:"devices,omitempty"`
	ProviderState json.RawMessage                `json:"providerState,omitempty"`
}

func NewPreparedClaim(claim *resourcev1.ResourceClaim, phase ClaimPhase) PreparedClaim {
	claimCopy := claim.DeepCopy()
	return PreparedClaim{
		UID:       string(claim.UID),
		Namespace: claim.Namespace,
		Name:      claim.Name,
		Labels:    maps.Clone(claim.Labels),
		Status:    claimCopy.Status,
		Phase:     phase,
	}
}

func (in PreparedClaim) DeepCopy() PreparedClaim {
	resourceClaim := (&resourcev1.ResourceClaim{Status: in.Status}).DeepCopy()
	out := in
	out.Labels = maps.Clone(in.Labels)
	out.Status = resourceClaim.Status
	out.Devices = slices.Clone(in.Devices)
	for i := range out.Devices {
		out.Devices[i].Requests = slices.Clone(in.Devices[i].Requests)
		out.Devices[i].CDIDeviceIDs = slices.Clone(in.Devices[i].CDIDeviceIDs)
	}
	out.ProviderState = slices.Clone(in.ProviderState)
	return out
}

func deepCopyClaims(in map[string]PreparedClaim) map[string]PreparedClaim {
	out := make(map[string]PreparedClaim, len(in))
	for uid, claim := range in {
		out[uid] = claim.DeepCopy()
	}
	return out
}
