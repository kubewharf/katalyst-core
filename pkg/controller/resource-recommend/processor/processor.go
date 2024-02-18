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

package processor

import (
	"context"

	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

type Processor interface {
	// Run performs the prediction routine.
	Run(ctx context.Context)

	Register(processConfig *processortypes.ProcessConfig) *errortypes.CustomError

	Cancel(processKey *processortypes.ProcessKey) *errortypes.CustomError

	QueryProcessedValues(taskKey *processortypes.ProcessKey) (float64, error)
}
