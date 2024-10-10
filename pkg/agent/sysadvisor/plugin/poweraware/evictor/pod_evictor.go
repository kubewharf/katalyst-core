/*
Copyright 2022 The Katalyst Authors.

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

package evictor

import (
	"context"

	v1 "k8s.io/api/core/v1"
)

// PodEvictor is the adapter interface for underlying eviction mechanism
type PodEvictor interface {
	Init() error
	Start() error
	Stop() error
	Evict(ctx context.Context, pods []*v1.Pod) error
}

// noopPodEvictor does not really evict any pod other than counting the invocations;
// used in unit test, or when eviction feature is disabled
type noopPodEvictor struct {
	called int
}

func (d *noopPodEvictor) Stop() error {
	return nil
}

func (d *noopPodEvictor) Start() error {
	return nil
}

func (d *noopPodEvictor) Init() error { return nil }

func (d *noopPodEvictor) Evict(ctx context.Context, pods []*v1.Pod) error {
	d.called += len(pods)
	return nil
}

func NewNoopPodEvictor() PodEvictor {
	return &noopPodEvictor{}
}
