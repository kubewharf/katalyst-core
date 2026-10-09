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

package qrm

import configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"

type CATPolicy struct {
	DefaultPlacement *CATPlacementPolicy
	ClosPlacements   map[string]CATPlacementPolicy
	ExclusiveClosIDs []string
}

type CATPlacementPolicy struct {
	AllowedBitUsages []CATBitUsage
	Direction        CATAllocationDirection
}

type CATBitUsage string

const (
	CATBitUsageAll       CATBitUsage = "*"
	CATBitUsageSoftware  CATBitUsage = "S"
	CATBitUsageHardware  CATBitUsage = "H"
	CATBitUsageExclusive CATBitUsage = "X"
)

type CATAllocationDirection string

const (
	CATAllocationDirectionLow  CATAllocationDirection = "low"
	CATAllocationDirectionHigh CATAllocationDirection = "high"
)

func mergeCATPolicyFromAPI(base CATPolicy, in *configv1alpha1.CATPolicy) CATPolicy {
	if in == nil {
		return base
	}

	out := CATPolicy{
		DefaultPlacement: base.DefaultPlacement,
		ClosPlacements:   base.ClosPlacements,
		ExclusiveClosIDs: base.ExclusiveClosIDs,
	}
	if in.DefaultPlacement != nil {
		placement := convertCATPlacementPolicyFromAPI(*in.DefaultPlacement)
		out.DefaultPlacement = &placement
	}
	if in.ClosPlacements != nil {
		out.ClosPlacements = make(map[string]CATPlacementPolicy, len(in.ClosPlacements))
		for key, placement := range in.ClosPlacements {
			out.ClosPlacements[key] = convertCATPlacementPolicyFromAPI(placement)
		}
	}
	if in.ExclusiveClosIDs != nil {
		out.ExclusiveClosIDs = append([]string(nil), (*in.ExclusiveClosIDs)...)
	}
	return out
}

func convertCATPlacementPolicyFromAPI(in configv1alpha1.CATPlacementPolicy) CATPlacementPolicy {
	return CATPlacementPolicy{
		AllowedBitUsages: convertCATBitUsagesFromAPI(in.AllowedBitUsages),
		Direction:        CATAllocationDirection(in.Direction),
	}
}

func convertCATBitUsagesFromAPI(in []configv1alpha1.CATBitUsage) []CATBitUsage {
	if in == nil {
		return nil
	}
	out := make([]CATBitUsage, len(in))
	for i := range in {
		out[i] = CATBitUsage(in[i])
	}
	return out
}
