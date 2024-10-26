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

package manager

import (
	"context"
	"runtime/debug"
	"sync"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	lister "github.com/kubewharf/katalyst-api/pkg/client/listers/recommendation/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/percentile"
	"github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/log"
)

type Manager struct {
	processors map[v1alpha1.Algorithm]processor.Processor
}

func NewManager(datasourceProxy *datasource.Proxy, lister lister.ResourceRecommendLister) *Manager {
	percentileProcessor := percentile.NewProcessor(datasourceProxy, lister)
	return &Manager{
		processors: map[v1alpha1.Algorithm]processor.Processor{
			v1alpha1.AlgorithmPercentile: percentileProcessor,
		},
	}
}

func (m *Manager) ProcessorRegister(algorithm v1alpha1.Algorithm, registerProcessor processor.Processor) {
	m.processors = make(map[v1alpha1.Algorithm]processor.Processor)
	m.processors[algorithm] = registerProcessor
}

func (m *Manager) StartProcess(ctx context.Context) {
	var wg sync.WaitGroup
	for processorName, dataProcessor := range m.processors {
		wg.Add(1)
		processorCtx := log.SetKeysAndValues(log.InitContext(ctx), "processor", processorName)
		go func(ctx context.Context, processor processor.Processor) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.ErrorS(ctx, r.(error), "processor panic", "stack", string(debug.Stack()))
					panic("processor panic")
				}
			}()
			processor.Run(ctx)
		}(processorCtx, dataProcessor)
	}

	log.InfoS(ctx, "ProcessorManager started, all Processor running")

	wg.Wait()

	log.InfoS(ctx, "ProcessorManager stopped, all Processor end")
}

func (m *Manager) GetProcessor(algorithm v1alpha1.Algorithm) processor.Processor {
	if dataProcessor, ok := m.processors[algorithm]; ok {
		return dataProcessor
	}
	return m.processors[v1alpha1.AlgorithmPercentile]
}
