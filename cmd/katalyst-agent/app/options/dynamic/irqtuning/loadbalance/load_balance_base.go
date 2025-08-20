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

package loadbalance

import (
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/loadbalance"
)

type IRQLoadBalanceOptions struct {
	// interval of two successive irq load balance MUST greater-equal this interval
	SuccessiveTuningInterval int
	Thresholds               *IRQLoadBalanceTuningThresholdOptions
	// two successive tunes whose interval is less-equal this threshold will be considered as pingpong tunings
	PingPongIntervalThreshold int
	// ping pong count greater-equal this threshold will trigger increasing irq cores
	PingPongCountThreshold int
	// max number of irqs are permitted to be tuned from some irq cores to other cores in each time, allowed value {1, 2}
	IRQTunedNumMaxEachTime int
	// max number of irq cores whose affinitied irqs are permitted to tuned to other cores in each time, allowed value {1,2}
	IRQCoresTunedNumMaxEachTime int
}

func NewIRQLoadBalanceOptions() *IRQLoadBalanceOptions {
	return &IRQLoadBalanceOptions{
		SuccessiveTuningInterval:    10,
		Thresholds:                  NewIRQLoadBalanceTuningThresholdOptions(),
		PingPongIntervalThreshold:   180,
		PingPongCountThreshold:      1,
		IRQTunedNumMaxEachTime:      2,
		IRQCoresTunedNumMaxEachTime: 1,
	}
}

func (o *IRQLoadBalanceOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("load-balance")

	fs.IntVar(&o.SuccessiveTuningInterval, "successive-tuning-interval", o.SuccessiveTuningInterval, "interval of two successive irq load balance MUST greater-equal this interval")
	fs.IntVar(&o.PingPongIntervalThreshold, "ping-pong-interval-threshold", o.PingPongIntervalThreshold, "ping pong count greater-equal this threshold will trigger increasing irq cores")
	fs.IntVar(&o.PingPongCountThreshold, "ping-pong-count-threshold", o.PingPongCountThreshold, "ping pong count greater-equal this threshold will trigger increasing irq cores")
	fs.IntVar(&o.IRQTunedNumMaxEachTime, "irq-tuned-num-max-each-time", o.IRQTunedNumMaxEachTime, "max number of irqs are permitted to be tuned from some irq cores to other cores in each time, allowed value {1, 2}")
	fs.IntVar(&o.IRQCoresTunedNumMaxEachTime, "irq-cores-tuned-num-max-each-time", o.IRQCoresTunedNumMaxEachTime, "max number of irq cores whose affinitied irqs are permitted to tuned to other cores in each time, allowed value {1,2}")

	o.Thresholds.AddFlags(fss)
}

func (o *IRQLoadBalanceOptions) ApplyTo(c *loadbalance.IRQLoadBalanceConfig) error {
	var errList []error
	c.SuccessiveTuningInterval = o.SuccessiveTuningInterval
	c.PingPongIntervalThreshold = o.PingPongIntervalThreshold
	c.PingPongCountThreshold = o.PingPongCountThreshold
	c.IRQsTunedNumMaxEachTime = o.IRQTunedNumMaxEachTime
	c.IRQCoresTunedNumMaxEachTime = o.IRQCoresTunedNumMaxEachTime

	errList = append(errList, o.Thresholds.ApplyTo(c.Thresholds))

	return errors.NewAggregate(errList)
}
