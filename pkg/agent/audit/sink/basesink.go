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

package sink

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/eventbus"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type BaseAuditSink struct {
	Interface
	eventbus eventbus.EventBus
}

func (b *BaseAuditSink) Run(ctx context.Context, bus eventbus.EventBus) {
	err := bus.Subscribe(consts.TopicNameApplyCGroup, b.GetName(), b.GetBufferSize(), b.GetHandler())
	if err != nil {
		general.Errorf("subscribe %v failed", consts.TopicNameApplyCGroup)
	}

	err = bus.Subscribe(consts.TopicNameApplyProcFS, b.GetName(), b.GetBufferSize(), b.GetHandler())
	if err != nil {
		general.Errorf("subscribe %v failed", consts.TopicNameApplyProcFS)
	}

	err = bus.Subscribe(consts.TopicNameSyscall, b.GetName(), b.GetBufferSize(), b.GetHandler())
	if err != nil {
		general.Errorf("subscribe %v failed", consts.TopicNameSyscall)
	}
	<-ctx.Done()
}
