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

package utils

import "github.com/pkg/errors"

const (
	// DefaultIRQExclusiveMaxExpansionRate identifies the maximum expansion ratio of the default irq exclusive.
	DefaultIRQExclusiveMaxExpansionRate = 0.3
	// DefaultIRQExclusiveMaxStepExpansionRate identifies the default irq exclusive maximum single-step expansion ratio.
	DefaultIRQExclusiveMaxStepExpansionRate = 0.05
)

var (
	ExceededMaxExpandableCapacityErr     = errors.New("exceeded the maximum expandable capacity")
	ExceededMaxStepExpandableCapacityErr = errors.New("exceeds the maximum number of expansions in a single step")
	ContainForbiddenCPUErr               = errors.New("contains forbidden cpu")
)
