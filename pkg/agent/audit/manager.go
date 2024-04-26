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

package audit

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/agent/audit/sink"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/eventbus"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var sinkInitializers = map[string]sink.Initializer{}

func RegisterSink(name string, initializer sink.Initializer) {
	sinkInitializers[name] = initializer
}

func GetSinkInitializers() map[string]sink.Initializer {
	return sinkInitializers
}

func init() {
	RegisterSink(sink.SinkNameLogBased, sink.NewLogBasedAuditSink)
}

type AuditManager struct {
	sinks    map[string]sink.Interface
	eventBus eventbus.EventBus
}

func NewAuditManager(conf *global.AuditConfiguration, eventbus eventbus.EventBus, emitter metrics.MetricEmitter) *AuditManager {
	sinks := map[string]sink.Interface{}
	initializers := GetSinkInitializers()
	for _, name := range conf.Sinks {
		if initializer, ok := initializers[name]; !ok {
			general.Errorf("unsupported sink: %s", name)
		} else {
			general.Infof("initialize for sink: %s", name)
			sinks[name] = initializer(conf, emitter)
		}
	}

	return &AuditManager{sinks: sinks, eventBus: eventbus}
}

func (m *AuditManager) Run(ctx context.Context) {
	for n, i := range m.sinks {
		general.Infof("start sink: %v", n)
		go i.Run(ctx, m.eventBus)
	}
	<-ctx.Done()
}
