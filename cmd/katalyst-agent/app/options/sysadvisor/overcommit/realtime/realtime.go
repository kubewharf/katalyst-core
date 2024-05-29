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

package realtime

import (
	"time"

	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/overcommit/realtime"
)

type RealtimeOvercommitOptions struct {
	SyncPeriod     time.Duration
	SyncPodTimeout time.Duration

	TargetCPULoad          float64
	TargetMemoryLoad       float64
	EstimatedPodCPULoad    float64
	EstimatedPodMemoryLoad float64

	CPUMetricsToGather    []string
	MemoryMetricsToGather []string
}

func NewRealtimeOvercommitOptions() *RealtimeOvercommitOptions {
	return &RealtimeOvercommitOptions{
		SyncPeriod:             10 * time.Second,
		SyncPodTimeout:         2 * time.Second,
		TargetCPULoad:          0.6,
		TargetMemoryLoad:       0.8,
		EstimatedPodCPULoad:    0.4,
		EstimatedPodMemoryLoad: 0.8,

		CPUMetricsToGather:    []string{},
		MemoryMetricsToGather: []string{},
	}
}

func (r *RealtimeOvercommitOptions) AddFlags(fs *pflag.FlagSet) {
	fs.DurationVar(&r.SyncPeriod, "realtime-overcommit-sync-period", r.SyncPeriod,
		"period for realtime overcommit advisor to calculate node resource overcommit ratio")
	fs.DurationVar(&r.SyncPodTimeout, "realtime-overcommit-sync-pod-timeout", r.SyncPodTimeout,
		"timeout for realtime overcommit advisor to list pod")
	fs.Float64Var(&r.TargetCPULoad, "realtime-overcommit-CPU-targetload", r.TargetCPULoad,
		"target node load for realtime overcommit advisor to calculate node CPU overcommit ratio")
	fs.Float64Var(&r.TargetMemoryLoad, "realtime-overcommit-mem-targetload", r.TargetMemoryLoad,
		"target node load for realtime overcommit advisor to calculate node memory overcommit ratio")
	fs.Float64Var(&r.EstimatedPodCPULoad, "realtime-overcommit-estimated-cpuload", r.EstimatedPodCPULoad,
		"estimated pod load for realtime overcommit advisor to calculate node CPU overcommit ratio")
	fs.Float64Var(&r.EstimatedPodMemoryLoad, "realtime-overcommit-estimated-memload", r.EstimatedPodMemoryLoad,
		"estimated pod load for realtime overcommit advisor to calculate node memory overcommit ratio")
	fs.StringSliceVar(&r.CPUMetricsToGather, "CPU-metrics-to-gather", r.CPUMetricsToGather,
		"metrics list used to calculate node cpu overcommitment ratio")
	fs.StringSliceVar(&r.MemoryMetricsToGather, "memory-metrics-to-gather", r.MemoryMetricsToGather,
		"metrics list used to calculate node memory overcommitment ratio")
}

func (r *RealtimeOvercommitOptions) ApplyTo(o *realtime.RealtimeOvercommitConfiguration) error {
	o.SyncPeriod = r.SyncPeriod
	o.SyncPodTimeout = r.SyncPodTimeout

	if r.TargetCPULoad > 0.0 && r.TargetMemoryLoad < 1.0 {
		o.TargetCPULoad = r.TargetCPULoad
	}
	if r.TargetMemoryLoad > 0.0 && r.TargetMemoryLoad < 1.0 {
		o.TargetMemoryLoad = r.TargetMemoryLoad
	}
	if r.EstimatedPodCPULoad > 0.0 && r.EstimatedPodCPULoad < 1.0 {
		o.EstimatedPodCPULoad = r.EstimatedPodCPULoad
	}
	if r.EstimatedPodMemoryLoad > 0.0 && r.EstimatedPodMemoryLoad < 1.0 {
		o.EstimatedPodMemoryLoad = r.EstimatedPodMemoryLoad
	}

	o.CPUMetricsToGather = r.CPUMetricsToGather
	o.MemoryMetricsToGather = r.MemoryMetricsToGather

	return nil
}
