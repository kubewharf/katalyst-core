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
	"fmt"
	"reflect"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func (s *workStatus) IsWorking() bool {
	return s.working
}

func validateWork(work *Work) (err error) {
	if work == nil {
		return fmt.Errorf("nil work")
	} else if work.Fn == nil {
		return fmt.Errorf("nil work Fn")
	} else if work.DeliveredAt.IsZero() {
		return fmt.Errorf("zero work DeliveredAt")
	}

	fnType := reflect.TypeOf(work.Fn)
	if fnType.Kind() != reflect.Func {
		return fmt.Errorf("work Fn isn't a function")
	} else if fnType.NumOut() != 1 || !fnType.Out(0).Implements(reflect.TypeOf(&err).Elem()) {
		return fmt.Errorf("work Fn doesn't return only an error, numOut: %d", fnType.NumOut())
	}

	return nil
}

// parseContextMetricEmitter parses and returns metrics.MetricEmitter object from given context
// returns false if not found
func parseContextMetricEmitter(ctx context.Context) (metrics.MetricEmitter, bool) {
	v := ctx.Value(contextKeyMetricEmitter)
	if v == nil {
		return metrics.DummyMetrics{}, false
	}

	if emitter, ok := v.(metrics.MetricEmitter); ok {
		return emitter, true
	}
	return metrics.DummyMetrics{}, false
}

// parseContextMetricName parses and returns metricName from given context
// returns false if not found
func parseContextMetricName(ctx context.Context) (string, bool) {
	v := ctx.Value(contextKeyMetricName)
	if v == nil {
		return "", false
	}

	if name, ok := v.(string); ok {
		return name, true
	}
	return "", false
}

// EmitAsyncedMetrics emit metrics through metricEmitter and metricName parsed from context
func EmitAsyncedMetrics(ctx context.Context, tags ...metrics.MetricTag) error {
	emitter, ok := parseContextMetricEmitter(ctx)
	if !ok {
		general.InfofV(4, "failed to get metrics-emitter")
		return nil
	}

	name, ok := parseContextMetricName(ctx)
	if !ok {
		general.InfofV(4, "failed to get metrics-name")
		return nil
	}

	return emitter.StoreInt64(name, 1, metrics.MetricTypeNameRaw, tags...)
}
