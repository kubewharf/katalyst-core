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

package capper

import (
	"context"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

// DynamicPowerCapper wraps a real PowerCapper and checks DisablePowerCapping
// from dynamic configuration on each Cap() call, falling back to noop when disabled.
// When transitioning from enabled to disabled, it resets the real power capper once to
// clear any existing capping effect on hardware.
type DynamicPowerCapper struct {
	conf       *dynamic.DynamicAgentConfiguration
	realCapper PowerCapper
	disabled   bool
}

func NewDynamicPowerCapper(conf *dynamic.DynamicAgentConfiguration, realCapper PowerCapper) PowerCapper {
	return &DynamicPowerCapper{
		conf:       conf,
		realCapper: realCapper,
	}
}

func (d *DynamicPowerCapper) Init() error  { return d.realCapper.Init() }
func (d *DynamicPowerCapper) Start() error { return d.realCapper.Start() }
func (d *DynamicPowerCapper) Stop() error  { return d.realCapper.Stop() }
func (d *DynamicPowerCapper) Reset()       { d.realCapper.Reset() }

func (d *DynamicPowerCapper) Cap(ctx context.Context, targetWatts, currWatt int) {
	if d.conf.GetDynamicConfiguration().DisablePowerCapping {
		klog.V(6).Infof("pap: power capping disabled, reset existing capping")
		d.resetOnce()
		return
	}

	d.disabled = false
	d.realCapper.Cap(ctx, targetWatts, currWatt)
}

func (d *DynamicPowerCapper) resetOnce() {
	if d.disabled {
		return
	}

	d.realCapper.Reset()
	d.disabled = true
}
