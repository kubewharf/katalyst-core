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
	"encoding/json"
	"errors"
	"time"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
)

type StateReader interface {
	ListPreparedClaims() map[string]state.PreparedClaim
}

// ResourceBackend implements resource-specific inventory collection and claim preparation.
type ResourceBackend interface {
	Start(context.Context, StateReader) error
	Stop() error
	Inventory(context.Context) (Inventory, error)
	Prepare(context.Context, *resourcev1.ResourceClaim) (PrepareResult, error)
	Unprepare(context.Context, state.PreparedClaim) error
}

type Inventory struct {
	PoolName string
	Devices  []resourcev1.Device
}

type PrepareResult struct {
	Devices       []state.PreparedDevice
	ProviderState json.RawMessage
}

// ResourceSlicePublisher is used to publish resource slices to the cluster. It also listens to its own ResourceSlice and reconciles.
type ResourceSlicePublisher interface {
	Start(context.Context, Inventory) error
	Stop()
}

type Options struct {
	StateDirectoryConfiguration *statedirectory.StateDirectoryConfiguration
	KubeClient                  kubernetes.Interface
	Backend                     ResourceBackend
	ResourceSlicePublisher      ResourceSlicePublisher
	RetryTimeout                time.Duration
}

type nonRetryableError struct {
	error
}

func (e nonRetryableError) Unwrap() error {
	return e.error
}

func NonRetryable(err error) error {
	if err == nil || IsNonRetryable(err) {
		return err
	}
	return nonRetryableError{error: err}
}

func IsNonRetryable(err error) bool {
	var target nonRetryableError
	return errors.As(err, &target)
}
