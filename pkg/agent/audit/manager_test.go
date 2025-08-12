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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/audit/sink"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/eventbus"
)

func TestAuditManager_Run(t *testing.T) {
	t.Parallel()

	auditConf := &global.AuditConfiguration{
		Sinks:      []string{sink.SinkNameLogBased},
		BufferSize: 1000,
	}

	bus := eventbus.NewEventBus(100)
	manager := NewAuditManager(auditConf, bus, metrics.DummyMetrics{})
	assert.NotNil(t, manager)

	ctx, cancelFunc := context.WithCancel(context.Background())
	go manager.Run(ctx)
	defer cancelFunc()

	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 20; i++ {
		err := bus.Publish(consts.TopicNameApplyCGroup, eventbus.RawCGroupEvent{})
		assert.NoError(t, err)
		err = bus.Publish(consts.TopicNameApplyProcFS, eventbus.RawCGroupEvent{})
		assert.NoError(t, err)
		err = bus.Publish(consts.TopicNameApplySysFS, eventbus.RawCGroupEvent{})
		assert.NoError(t, err)
		err = bus.Publish(consts.TopicNameSyscall, eventbus.RawCGroupEvent{})
		assert.NoError(t, err)
	}

	time.Sleep(300 * time.Millisecond)
}
