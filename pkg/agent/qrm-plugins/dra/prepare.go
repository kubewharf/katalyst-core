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
	"errors"
	"fmt"
	"slices"
	"time"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	drapluginapi "k8s.io/kubelet/pkg/apis/dra/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
)

type claimReference struct {
	UID       string
	Namespace string
	Name      string
}

func claimRefValue(namespace, name, uid string) claimReference {
	return claimReference{UID: uid, Namespace: namespace, Name: name}
}

// NodePrepareResources implements DRAPluginServer.NodePrepareResources.
func (p *StaticPolicy) NodePrepareResources(
	ctx context.Context,
	request *drapluginapi.NodePrepareResourcesRequest,
) (*drapluginapi.NodePrepareResourcesResponse, error) {
	type result struct {
		uid      string
		response *drapluginapi.NodePrepareResourceResponse
	}

	refs := request.GetClaims()
	results := make(chan result, len(refs))
	for _, ref := range refs {
		go func(ref *drapluginapi.Claim) {
			results <- result{
				uid:      ref.GetUid(),
				response: p.prepareClaim(ctx, ref),
			}
		}(ref)
	}

	response := &drapluginapi.NodePrepareResourcesResponse{
		Claims: make(
			map[string]*drapluginapi.NodePrepareResourceResponse,
			len(refs),
		),
	}
	for range refs {
		result := <-results
		response.Claims[result.uid] = result.response
	}
	return response, nil
}

func (p *StaticPolicy) prepareClaim(
	ctx context.Context,
	ref *drapluginapi.Claim,
) *drapluginapi.NodePrepareResourceResponse {
	devices, err := runWithRetry(
		ctx,
		p.retryTimeout,
		ref.GetUid(),
		func(attemptCtx context.Context) ([]state.PreparedDevice, error) {
			claim, err := p.resolveClaim(attemptCtx, ref)
			if err != nil {
				return nil, err
			}
			return p.prepareOne(attemptCtx, claim)
		},
	)
	if err != nil {
		return &drapluginapi.NodePrepareResourceResponse{Error: err.Error()}
	}

	preparedDevices := make([]*drapluginapi.Device, len(devices))
	for i := range devices {
		preparedDevices[i] = preparedDeviceResponse(devices[i])
	}
	return &drapluginapi.NodePrepareResourceResponse{Devices: preparedDevices}
}

// NodeUnprepareResources implements DRAPluginServer.NodeUnprepareResources.
func (p *StaticPolicy) NodeUnprepareResources(
	ctx context.Context,
	request *drapluginapi.NodeUnprepareResourcesRequest,
) (*drapluginapi.NodeUnprepareResourcesResponse, error) {
	type result struct {
		uid      string
		response *drapluginapi.NodeUnprepareResourceResponse
	}

	refs := request.GetClaims()
	results := make(chan result, len(refs))
	for _, requestRef := range refs {
		go func(requestRef *drapluginapi.Claim) {
			results <- result{
				uid:      requestRef.GetUid(),
				response: p.unprepareClaim(ctx, requestRef),
			}
		}(requestRef)
	}

	response := &drapluginapi.NodeUnprepareResourcesResponse{
		Claims: make(
			map[string]*drapluginapi.NodeUnprepareResourceResponse,
			len(refs),
		),
	}
	for range refs {
		result := <-results
		response.Claims[result.uid] = result.response
	}
	return response, nil
}

func (p *StaticPolicy) unprepareClaim(
	ctx context.Context,
	requestRef *drapluginapi.Claim,
) *drapluginapi.NodeUnprepareResourceResponse {
	ref := claimRefValue(
		requestRef.GetNamespace(),
		requestRef.GetName(),
		requestRef.GetUid(),
	)
	_, err := runWithRetry(
		ctx,
		p.retryTimeout,
		ref.UID,
		func(attemptCtx context.Context) (struct{}, error) {
			return struct{}{}, p.unprepareOne(attemptCtx, ref)
		},
	)
	response := &drapluginapi.NodeUnprepareResourceResponse{}
	if err != nil {
		response.Error = err.Error()
	}
	return response
}

func (p *StaticPolicy) prepareOne(
	ctx context.Context,
	claim *resourcev1.ResourceClaim,
) ([]state.PreparedDevice, error) {
	p.operationMu.Lock()
	defer p.operationMu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Check if the claim has been prepared before.
	if devices, prepared, err := p.completedPrepare(claim.UID); prepared || err != nil {
		return devices, err
	}

	// Return an error if there is overlap with other prepared claims.
	if err := p.validateNoOverlap(claim); err != nil {
		return nil, NonRetryable(err)
	}

	prepared, err := p.beginPrepare(claim)
	if err != nil {
		return nil, err
	}
	result, err := p.backend.Prepare(ctx, claim)
	if err != nil {
		return nil, err
	}
	if err := p.completePrepare(prepared, result); err != nil {
		return nil, err
	}
	return slices.Clone(result.Devices), nil
}

func (p *StaticPolicy) completedPrepare(
	uid types.UID,
) ([]state.PreparedDevice, bool, error) {
	prepared, found := p.state.GetPreparedClaim(string(uid))
	if !found || prepared.Phase != state.PrepareCompleted {
		return nil, false, nil
	}
	if err := p.state.StoreState(); err != nil {
		return nil, true, fmt.Errorf(
			"checkpoint existing PrepareCompleted: %w",
			err,
		)
	}
	return slices.Clone(prepared.Devices), true, nil
}

func (p *StaticPolicy) beginPrepare(
	claim *resourcev1.ResourceClaim,
) (state.PreparedClaim, error) {
	prepared := state.NewPreparedClaim(claim, state.PrepareStarted)
	if err := p.state.SetPreparedClaim(prepared, true); err != nil {
		return state.PreparedClaim{}, fmt.Errorf(
			"checkpoint PrepareStarted: %w",
			err,
		)
	}
	return prepared, nil
}

func (p *StaticPolicy) completePrepare(
	prepared state.PreparedClaim,
	result PrepareResult,
) error {
	prepared.Phase = state.PrepareCompleted
	prepared.Devices = result.Devices
	prepared.ProviderState = result.ProviderState
	if err := p.state.SetPreparedClaim(prepared, true); err != nil {
		return fmt.Errorf("checkpoint PrepareCompleted: %w", err)
	}
	return nil
}

func (p *StaticPolicy) validateNoOverlap(claim *resourcev1.ResourceClaim) error {
	for uid, prepared := range p.state.ListPreparedClaims() {
		if uid == string(claim.UID) {
			continue
		}
		if device, found := overlappingDevice(
			claim.Status.Allocation,
			prepared.Status.Allocation,
		); found {
			return fmt.Errorf(
				"device %s/%s/%s is already prepared for ResourceClaim %s/%s",
				device.Driver,
				device.Pool,
				device.Device,
				prepared.Namespace,
				prepared.Name,
			)
		}
	}
	return nil
}

func overlappingDevice(
	left, right *resourcev1.AllocationResult,
) (resourcev1.DeviceRequestAllocationResult, bool) {
	if left == nil || right == nil {
		return resourcev1.DeviceRequestAllocationResult{}, false
	}
	for _, leftDevice := range left.Devices.Results {
		if isAdminAccess(leftDevice) {
			continue
		}
		for _, rightDevice := range right.Devices.Results {
			if isAdminAccess(rightDevice) {
				continue
			}
			if sameDevice(leftDevice, rightDevice) {
				return leftDevice, true
			}
		}
	}
	return resourcev1.DeviceRequestAllocationResult{}, false
}

func isAdminAccess(device resourcev1.DeviceRequestAllocationResult) bool {
	return device.AdminAccess != nil && *device.AdminAccess
}

func sameDevice(
	left, right resourcev1.DeviceRequestAllocationResult,
) bool {
	return left.Driver == right.Driver &&
		left.Pool == right.Pool &&
		left.Device == right.Device
}

func (p *StaticPolicy) unprepareOne(ctx context.Context, ref claimReference) error {
	p.operationMu.Lock()
	defer p.operationMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	prepared, err := p.preparedClaimForUnprepare(ref.UID)
	if err != nil || prepared == nil {
		return err
	}
	if err := p.backend.Unprepare(ctx, *prepared); err != nil {
		return err
	}
	if err := p.state.DeletePreparedClaim(ref.UID, true); err != nil {
		deleteErr := fmt.Errorf("delete prepared claim checkpoint: %w", err)
		if restoreErr := p.state.SetPreparedClaim(*prepared, false); restoreErr != nil {
			return errors.Join(
				deleteErr,
				fmt.Errorf("restore prepared claim in memory: %w", restoreErr),
			)
		}
		return deleteErr
	}
	return nil
}

func (p *StaticPolicy) preparedClaimForUnprepare(
	uid string,
) (*state.PreparedClaim, error) {
	prepared, found := p.state.GetPreparedClaim(uid)
	if !found {
		return nil, p.state.StoreState()
	}
	return &prepared, nil
}

func runWithRetry[T any](
	ctx context.Context,
	timeout time.Duration,
	key string,
	fn func(context.Context) (T, error),
) (T, error) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	queue := workqueue.NewNamedRateLimitingQueue(
		workqueue.DefaultControllerRateLimiter(),
		"dra-"+key,
	)
	defer queue.ShutDown()
	go func() {
		<-requestCtx.Done()
		queue.ShutDown()
	}()

	queue.Add(key)
	for {
		item, shutdown := queue.Get()
		if shutdown {
			var zero T
			return zero, requestCtx.Err()
		}

		result, err := fn(requestCtx)
		queue.Done(item)
		if err == nil {
			queue.Forget(item)
			return result, nil
		}
		if IsNonRetryable(err) {
			queue.Forget(item)
			var zero T
			return zero, err
		}
		queue.AddRateLimited(item)
	}
}
