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

package tuner

import (
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type IRQTunerStub struct {
	irqtuner.StateAdapter
	conf *config.Configuration
}

func NewIRQTunerStub(conf *config.Configuration, sa irqtuner.StateAdapter) irqtuner.Tuner {
	return &IRQTunerStub{sa, conf}
}

func (t *IRQTunerStub) Run(stopCh <-chan struct{}) {
	//general.RegisterHeartbeatCheck(cpuconsts.IRQTuning, 2*time.Minute, general.HealthzCheckStateNotReady, 2*time.Minute)
	// t.run()
}

func (t *IRQTunerStub) Stop() {

}

func (t *IRQTunerStub) run() {
	// set irq exclusive cpu set
	cpuSet := []int{22, 23}
	err := t.SetExclusiveIRQCPUSet(machine.NewCPUSet(cpuSet...))
	if err != nil {
		general.Errorf("set exclusive IRQ CPUSet failed with error: %v", err)
	}

	// apply irq exclusive cpu set
	err = manager.ApplyProcInterrupts(38, machine.NewCPUSet(cpuSet...).String())
	if err != nil {
		general.Errorf("ApplyProcInterrupts failed with error: %v", err)
	}

	go t.getDynamicConf()
	go t.tunerStateGet()
}

func (t *IRQTunerStub) getDynamicConf() {
	for {
		dc := t.conf.AgentConfiguration.DynamicAgentConfiguration.GetDynamicConfiguration()
		if dc == nil {
			general.Errorf("IRQTunerStub getDynamicConf get nil")
			continue
		}
		general.Infof("IRQTunerStub getDynamicConf get dc:%+v", *dc)

		time.Sleep(5 * time.Second)
	}
}

func (t *IRQTunerStub) tunerStateGet() {
	for {
		cs, err := t.ListContainers()
		if err != nil {
			general.Errorf("listing containers info failed: %v", err)
		} else {
			general.Infof("get containers info: %v", cs)
		}

		// get forbidden cores
		irqForbiddenCPUs, err := t.GetIRQForbiddenCores()
		if err != nil {
			general.Errorf("get irq forbidden CPUs: %v", err)
		} else {
			general.Infof("get irq forbidden CPUs: %v", irqForbiddenCPUs)
		}

		// get
		stepMax := t.GetStepExpandableCPUsMax()
		general.Infof("get step max: %v", stepMax)

		// get exclusive IRQ CPUSet
		irqExclusiveCPUs, err := t.GetExclusiveIRQCPUSet()
		if err != nil {
			general.Errorf("get exclusive IRQ CPUSet failed with error: %v", err)
		} else {
			general.Infof("get exclusive IRQ CPUSet: %v", irqExclusiveCPUs)
		}

		general.Infof("[DEBUG] irq tuner stub get sleep...")
		time.Sleep(5 * time.Second)
	}
}
