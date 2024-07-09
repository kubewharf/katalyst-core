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

package options

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
)

const (
	defaultNodeOvercommitSyncWorkers     = 1
	defaultNodeOvercommitReconcilePeriod = 30 * time.Minute
)

// OvercommitOptions holds the configurations for overcommit.
type OvercommitOptions struct {
	NodeOvercommitOptions
	PredictionOptions
}

type PredictionOptions struct {
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

	prometheus.PromConfig
	NSigmaOptions
}

type NSigmaOptions struct {
	Factor  int
	Buckets int
}

// NodeOvercommitOptions holds the configurations for nodeOvercommitConfig controller.
type NodeOvercommitOptions struct {
	// numer of workers to sync overcommit config
	SyncWorkers int

	// time interval of reconcile overcommit config
	ConfigReconcilePeriod time.Duration
}

// NewOvercommitOptions creates a new Options with a default config.
func NewOvercommitOptions() *OvercommitOptions {
	return &OvercommitOptions{
		PredictionOptions: PredictionOptions{
			EnablePredict:          false,
			Predictor:              "",
			PredictPeriod:          24 * time.Hour,
			ReconcilePeriod:        1 * time.Hour,
			MaxTimeSeriesDuration:  7 * 24 * time.Hour,
			MinTimeSeriesDuration:  24 * time.Hour,
			CPUScaleFactor:         1,
			MemoryScaleFactor:      1,
			NodeCPUTargetLoad:      0.6,
			NodeMemoryTargetLoad:   0.8,
			PodEstimatedCPULoad:    0.5,
			PodEstimatedMemoryLoad: 0.8,
			NSigmaOptions: NSigmaOptions{
				Factor:  3,
				Buckets: 24,
			},
			PromConfig: prometheus.PromConfig{},
		},
	}
}

// AddFlags adds flags to the specified FlagSet
func (o *OvercommitOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("noc")

	fs.IntVar(&o.SyncWorkers, "nodeovercommit-sync-workers", defaultNodeOvercommitSyncWorkers, "num of goroutines to sync nodeovercommitconfig")
	fs.DurationVar(&o.ConfigReconcilePeriod, "nodeovercommit-reconcile-period", defaultNodeOvercommitReconcilePeriod, "Period for nodeovercommit controller to sync configs")

	fs.BoolVar(&o.EnablePredict, "nodeovercommit-enable-predict", o.EnablePredict, "enable node overcommit prediction")
	fs.StringVar(&o.Predictor, "nodeovercommit-predictor", o.Predictor, "workload usage predictor in node overcommit controller")
	fs.DurationVar(&o.PredictPeriod, "nodeovercommit-workload-predict-period", o.PredictPeriod, "reconcile period of workload usage predictor in overcommit controller")
	fs.DurationVar(&o.ReconcilePeriod, "nodeovercommit-node-predict-period", o.ReconcilePeriod, "reconcile period of node overcommitmentRatio prediction in overcommit controller")
	fs.DurationVar(&o.MaxTimeSeriesDuration, "nodeovercommit-max-timeseries-duration", o.MaxTimeSeriesDuration,
		"max time duration of time series for workload usage prediction, default 7 days")
	fs.DurationVar(&o.MinTimeSeriesDuration, "nodeovercommit-min-timeseries-duration", o.MinTimeSeriesDuration,
		"min time duration of time series for workload usage prediction, default 24 hours")
	fs.IntVar(&o.Factor, "nodeovercommit-nsigma-factor", o.Factor, "stddev factor of n-sigma predictor, default 3")
	fs.IntVar(&o.Buckets, "nodeovercommit-nsigma-buckets", o.Buckets,
		"bucket of n-sigma predictor result, 24 means predictor result will be divide into 24 buckets according to hours")
	fs.StringVar(&o.TargetReferenceNameKey, "nodeovercommit-target-reference-name-key", o.TargetReferenceNameKey,
		"overcommit controller get pod owner reference workload name from pod label by nodeovercommit-target-reference-name-key")
	fs.StringVar(&o.TargetReferenceTypeKey, "nodeovercommit-target-reference-type-key", o.TargetReferenceTypeKey,
		"overcommit controller get pod owner reference workload type from pod label by nodeovercommit-target-reference-type-key")
	fs.Float64Var(&o.CPUScaleFactor, "nodeovercommit-cpu-scaleFactor", o.CPUScaleFactor,
		"podUsage = podRequest * scaleFactor when pod resource portrait is missed")
	fs.Float64Var(&o.MemoryScaleFactor, "nodeovercommit-memory-scaleFactor", o.MemoryScaleFactor,
		"podUsage = podRequest * scaleFactor when pod resource portrait is missed")

	fs.Float64Var(&o.NodeCPUTargetLoad, "nodeovercommit-cpu-targetload", o.NodeCPUTargetLoad,
		"max node CPU load when calculate node CPU overcommitment ratio, should be greater than 0 and less than 1")
	fs.Float64Var(&o.NodeMemoryTargetLoad, "nodeovercommit-memory-targetload", o.NodeMemoryTargetLoad,
		"max node memory load when calculate node CPU overcommitment ratio, should be greater than 0 and less than 1")
	fs.Float64Var(&o.PodEstimatedCPULoad, "nodeovercommit-cpu-estimatedload", o.PodEstimatedCPULoad,
		"estimated avg pod CPU load in the cluster, should be greater than 0 and less than 1")
	fs.Float64Var(&o.PodEstimatedMemoryLoad, "nodeovercommit-memory-estimatedload", o.PodEstimatedMemoryLoad,
		"estimated avg pod memory load in the cluster, should be greater than 0 and less than 1")

	fs.StringVar(&o.Address, "nodeovercommit-prometheus-address", "", "prometheus address")
	fs.StringVar(&o.Auth.Type, "nodeovercommit-prometheus-auth-type", "", "prometheus auth type")
	fs.StringVar(&o.Auth.Username, "nodeovercommit-prometheus-auth-username", "", "prometheus auth username")
	fs.StringVar(&o.Auth.Password, "nodeovercommit-prometheus-auth-password", "", "prometheus auth password")
	fs.StringVar(&o.Auth.BearerToken, "nodeovercommit-prometheus-auth-bearertoken", "", "prometheus auth bearertoken")
	fs.DurationVar(&o.KeepAlive, "nodeovercommit-prometheus-keepalive", 60*time.Second, "prometheus keep alive")
	fs.DurationVar(&o.Timeout, "nodeovercommit-prometheus-timeout", 3*time.Minute, "prometheus timeout")
	fs.BoolVar(&o.BRateLimit, "nodeovercommit-prometheus-bratelimit", false, "prometheus bratelimit")
	fs.IntVar(&o.MaxPointsLimitPerTimeSeries, "nodeovercommit-prometheus-maxpoints", 11000, "prometheus max points limit per time series")
	fs.StringVar(&o.BaseFilter, "nodeovercommit-prometheus-promql-base-filter", "", ""+
		"Get basic filters in promql for historical usage data. This filter is added to all promql statements. "+
		"Supports filters format of promql, e.g: group=\\\"Katalyst\\\",cluster=\\\"cfeaf782fasdfe\\\"")
	fs.BoolVar(&o.InsecureSkipVerify, "nodeovercommit-prometheus-insecureSkipVerify", true, "prometheus insecure skip verify")
	fs.DurationVar(&o.TLSHandshakeTimeoutInSecond, "nodeovercommit-prometheus-TLSHandshakeTimeoutInSecond", 10*time.Second, "prometheus TLSHandshake timeout")
}

func (o *OvercommitOptions) ApplyTo(c *controller.OvercommitConfig) error {
	c.Node.SyncWorkers = o.SyncWorkers
	c.Node.ConfigReconcilePeriod = o.ConfigReconcilePeriod
	c.Prediction.EnablePredict = o.EnablePredict
	c.Prediction.Predictor = o.Predictor
	c.Prediction.PredictPeriod = o.PredictPeriod
	c.Prediction.ReconcilePeriod = o.ReconcilePeriod
	c.Prediction.MaxTimeSeriesDuration = o.MaxTimeSeriesDuration
	c.Prediction.MinTimeSeriesDuration = o.MinTimeSeriesDuration
	c.Prediction.Buckets = o.Buckets
	c.Prediction.Factor = o.Factor
	c.Prediction.TargetReferenceNameKey = o.TargetReferenceNameKey
	c.Prediction.TargetReferenceTypeKey = o.TargetReferenceTypeKey
	c.Prediction.CPUScaleFactor = o.CPUScaleFactor
	c.Prediction.MemoryScaleFactor = o.MemoryScaleFactor
	c.Prediction.NodeCPUTargetLoad = o.NodeCPUTargetLoad
	c.Prediction.NodeMemoryTargetLoad = o.NodeMemoryTargetLoad
	c.Prediction.PodEstimatedCPULoad = o.PodEstimatedCPULoad
	c.Prediction.PodEstimatedMemoryLoad = o.PodEstimatedMemoryLoad
	c.Prediction.PromConfig = &o.PromConfig
	return nil
}

func (o *OvercommitOptions) Config() (*controller.OvercommitConfig, error) {
	c := &controller.OvercommitConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
