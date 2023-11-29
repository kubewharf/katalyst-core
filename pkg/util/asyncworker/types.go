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

package asyncworker

import (
	"context"
	"sync"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// WorkNameSeperator is used to assemble standard work-name
// and we have assumptions below, for work-name 'a/b/c':
// - 'a' and 'b' are specified identifiers for objects/triggers(etc.) on the action
// - 'c' is a general identifier for actions
const WorkNameSeperator = "/"

type contextKey string

const (
	contextKeyMetricEmitter contextKey = "metric_emitter"
	contextKeyMetricName    contextKey = "metric_name"
)

// workStatus tracks worker is working or not
// and containers context to cancel work
type workStatus struct {
	// ctx is the context that is associated with the current pod sync
	ctx context.Context
	// cancelFn if set is expected to cancel the current work
	cancelFn context.CancelFunc
	// working is true if the worker is working
	working bool
	// startAt is the time at which the worker starts working
	startedAt time.Time
	// work is being handled,
	// only set when property working is true
	work *Work
}

// Work contains details to handle by workers
type Work struct {
	// Fn is the function to handle the work,
	// its first param must be of type context.Context,
	// it's allowed to have any number of other parameters of any type,
	// and can have only one return value with error type,
	// its prototype likes func(ctx context.Context, i int, s string, ...) error
	Fn interface{}
	// Params are parameters of the fn
	Params []interface{}
	// DeliverAt is the time at which the work is delivered
	DeliveredAt time.Time
}

type AsyncWorkers struct {
	// name of AsyncWorkers
	name    string
	emitter metrics.MetricEmitter
	// Protects all per work fields
	workLock sync.Mutex
	// Tracks the last undelivered work item of corresponding work name - a work item is
	// undelivered if it comes in while the worker is working
	lastUndeliveredWork map[string]*Work
	// Tracks work status by work name
	workStatuses map[string]*workStatus
}
