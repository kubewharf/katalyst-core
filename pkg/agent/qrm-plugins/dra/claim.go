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

package dra

import (
	"context"
	"fmt"
	"slices"
	"strings"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	drapluginapi "k8s.io/kubelet/pkg/apis/dra/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
)

// resolveClaim ensures that a claim still exists.
func (p *StaticPolicy) resolveClaim(
	ctx context.Context,
	ref *drapluginapi.Claim,
) (*resourcev1.ResourceClaim, error) {
	claim, err := p.kubeClient.ResourceV1().
		ResourceClaims(ref.GetNamespace()).
		Get(ctx, ref.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf(
			"get ResourceClaim %s/%s: %w",
			ref.GetNamespace(),
			ref.GetName(),
			err,
		)
	}
	if claim.UID != types.UID(ref.GetUid()) {
		return nil, NonRetryable(fmt.Errorf(
			"ResourceClaim %s/%s was replaced",
			claim.Namespace,
			claim.Name,
		))
	}
	if claim.Status.Allocation == nil {
		return nil, NonRetryable(fmt.Errorf(
			"ResourceClaim %s/%s is not allocated",
			claim.Namespace,
			claim.Name,
		))
	}
	return claim, nil
}

func preparedDeviceResponse(in state.PreparedDevice) *drapluginapi.Device {
	requests := make([]string, len(in.Requests))
	for i, request := range in.Requests {
		requests[i] = baseRequestRef(request)
	}
	return &drapluginapi.Device{
		RequestNames: requests,
		PoolName:     in.PoolName,
		DeviceName:   in.DeviceName,
		CdiDeviceIds: slices.Clone(in.CDIDeviceIDs),
	}
}

func baseRequestRef(requestRef string) string {
	request, _, _ := strings.Cut(requestRef, "/")
	return request
}
