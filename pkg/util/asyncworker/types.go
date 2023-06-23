package asyncworker

import (
	"context"
	"sync"
	"time"
)

// worStatus tracks worker is working or not
// and containers context to cancel work
type workStatus struct {
	// ctx is the context that is associated with the current pod sync.
	ctx context.Context
	// cancelFn if set is expected to cancel the current work.
	cancelFn context.CancelFunc
	// working is true if the worker is working.
	working bool
	// startAt is the time at which the worker starts working.
	startedAt time.Time
	// work is being handled,
	// only set when property working is true
	work *Work
}

// work contains details to handle by workers
type Work struct {
	// Fn is the function to handle the work
	Fn WorkFunc
	// Params are parameters of the fn
	Params []interface{}
	// DeliverAt is the time at which the work is delivered.
	DeliveredAt time.Time
}

type WorkFunc func(ctx context.Context, params ...interface{}) error

type AsyncWorkers struct {
	// name of AsyncWorkers
	name string
	// Protects all per work fields.
	workLock sync.Mutex
	// Tracks the last undelivered work item of corresponding work name - a work item is
	// undelivered if it comes in while the worker is working.
	lastUndeliveredWork map[string]*Work
	// Tracks work status by work name
	workStatuses map[string]*workStatus
}
