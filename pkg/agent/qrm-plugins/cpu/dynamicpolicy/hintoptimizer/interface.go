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

package hintoptimizer

import (
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

// Request wraps the original ResourceRequest and includes the CPU request amount.
type Request struct {
	*pluginapi.ResourceRequest
	CPURequest float64
}

// HintOptimizer is the interface for optimizing topology hints.
type HintOptimizer interface {
	// OptimizeHints optimizes the given list of topology hints based on the current state and request.
	OptimizeHints(Request, *pluginapi.ListOfTopologyHints) error
	// Run starts the hint optimizer.
	Run(stopCh <-chan struct{}) error
}

// DummyHintOptimizer is a no-op implementation of HintOptimizer.
type DummyHintOptimizer struct{}

var _ HintOptimizer = &DummyHintOptimizer{}

// OptimizeHints for DummyHintOptimizer does nothing and returns nil.
func (d *DummyHintOptimizer) OptimizeHints(Request, *pluginapi.ListOfTopologyHints) error {
	return nil
}

// Run for DummyHintOptimizer does nothing.
func (d *DummyHintOptimizer) Run(_ <-chan struct{}) error {
	return nil
}
