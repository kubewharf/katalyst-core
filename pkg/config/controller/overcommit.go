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

package controller

import (
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
)

type OvercommitConfig struct {
	Node NodeOvercommitConfig

	Prediction PredictionConfig
}

type NodeOvercommitConfig struct {
	// number of workers to sync overcommit config
	SyncWorkers int

	// time interval of reconcile overcommit config
	ConfigReconcilePeriod time.Duration
}

func NewOvercommitConfig() *OvercommitConfig {
	return &OvercommitConfig{
		Node: NodeOvercommitConfig{},
		Prediction: PredictionConfig{
			PromConfig: &prometheus.PromConfig{},
		},
	}
}

type PredictionConfig struct {
	EnablePredict   bool
	Predictor       string
	PredictPeriod   time.Duration
	ReconcilePeriod time.Duration

	MaxTimeSeriesDuration time.Duration
	MinTimeSeriesDuration time.Duration

	TargetReferenceNameKey string
	TargetReferenceTypeKey string
	CPUScaleFactor         float64
	MemoryScaleFactor      float64

	NodeCPUTargetLoad      float64
	NodeMemoryTargetLoad   float64
	PodEstimatedCPULoad    float64
	PodEstimatedMemoryLoad float64

	*prometheus.PromConfig
	NSigmaPredictorConfig
}

type NSigmaPredictorConfig struct {
	Factor  int
	Buckets int
}
