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

package planner

import "fmt"

// PendingAdviceSnapshot captures the three freshness fence values that were
// observed when an advice snapshot became pending.
//
// The three-layer fence requires the advisor token, in-memory CPU state
// revision, and normalized request hash to all match the current
// AdviceFreshness before the pending advice may be applied. The zero value is a
// valid initial snapshot and matches a zero-value AdviceFreshness.
type PendingAdviceSnapshot struct {
	Token                 uint64
	InMemoryRevision      uint64
	NormalizedRequestHash uint64
}

// AdviceFreshness describes the current three-layer freshness fence for advice
// validation.
//
// Token fences the advisor generation, InMemoryRevision fences the in-memory
// CPU state generation, and NormalizedRequestHash fences the normalized request
// shape. The zero value is a valid initial freshness value and matches a
// zero-value PendingAdviceSnapshot.
type AdviceFreshness struct {
	Token                 uint64
	InMemoryRevision      uint64
	NormalizedRequestHash uint64
}

// Validate reports whether p still matches current across the three-layer
// freshness fence.
//
// A nil error means the advisor token, in-memory revision, and normalized
// request hash all match, including the legal initial case where both sides are
// zero values. Mismatches are returned as ordinary errors; this method does not
// currently commit to a typed error contract.
func (p PendingAdviceSnapshot) Validate(current AdviceFreshness) error {
	if p.Token != current.Token {
		return fmt.Errorf("advice freshness token mismatch: pending=%d current=%d", p.Token, current.Token)
	}
	if p.InMemoryRevision != current.InMemoryRevision {
		return fmt.Errorf("advice freshness in-memory revision mismatch: pending=%d current=%d", p.InMemoryRevision, current.InMemoryRevision)
	}
	if p.NormalizedRequestHash != current.NormalizedRequestHash {
		return fmt.Errorf("advice freshness normalized request hash mismatch: pending=%d current=%d", p.NormalizedRequestHash, current.NormalizedRequestHash)
	}

	return nil
}
