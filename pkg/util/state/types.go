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

package state

import "k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

// Storable is an interface that knows how to restore state from a checkpoint and initialize a new checkpoint
type Storable interface {
	// RestoreState knows how to restore state from a checkpoint
	RestoreState(cp checkpointmanager.Checkpoint) (bool, error)

	// InitNewCheckpoint knows how to initialize an empty or non-empty new checkpoint
	InitNewCheckpoint(empty bool) checkpointmanager.Checkpoint
}
