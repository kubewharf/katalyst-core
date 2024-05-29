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
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/oom"
	processormanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/manager"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/recommender"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/recommender/recommenders"
)

type Manager struct {
	ProcessorManager processormanager.Manager
	OomRecorder      oom.Recorder
}

func NewManager(ProcessorManager processormanager.Manager, OomRecorder oom.Recorder) *Manager {
	return &Manager{
		ProcessorManager: ProcessorManager,
		OomRecorder:      OomRecorder,
	}
}

func (m *Manager) NewRecommender(algorithm v1alpha1.Algorithm) recommender.Recommender {
	switch algorithm {
	case v1alpha1.AlgorithmPercentile:
		return recommenders.NewPercentileRecommender(m.ProcessorManager.GetProcessor(v1alpha1.AlgorithmPercentile), m.OomRecorder)
	}
	klog.InfoS("no recommender matched. fall through to default percentile recommender")
	return recommenders.NewPercentileRecommender(m.ProcessorManager.GetProcessor(v1alpha1.AlgorithmPercentile), m.OomRecorder)
}
