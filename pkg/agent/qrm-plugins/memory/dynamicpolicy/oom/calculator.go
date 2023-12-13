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

package oom

import (
	"fmt"
	"sync"

	core "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

type OOMPriorityCalculator func(qosConfig *generic.QoSConfiguration, pod *core.Pod, previousPriority int) (int, error)

var (
	oomPriorityCalculatorsMtx sync.RWMutex
	oomPriorityCalculators    = []OOMPriorityCalculator{
		nativeOOMPriorityCalculator,
	}
)

func AppendOOMPriorityCalculator(calculator OOMPriorityCalculator) {
	oomPriorityCalculatorsMtx.Lock()
	defer oomPriorityCalculatorsMtx.Unlock()
	oomPriorityCalculators = append(oomPriorityCalculators, calculator)
}

func GetOOMPriority(qosConfig *generic.QoSConfiguration, pod *core.Pod) (oomPriority int, err error) {
	oomPriorityCalculatorsMtx.RLock()
	defer oomPriorityCalculatorsMtx.RUnlock()

	for _, calculator := range oomPriorityCalculators {
		oomPriority, err = calculator(qosConfig, pod, oomPriority)
		if err != nil {
			return 0, fmt.Errorf("calculator %T failed: %w", calculator, err)
		}
	}
	return
}

func nativeOOMPriorityCalculator(qosConfig *generic.QoSConfiguration, pod *core.Pod, _ int) (int, error) {
	qosLevel, err := qosConfig.GetQoSLevelForPod(pod)
	if err != nil {
		return 0, err
	}

	userSpecifiedOOMPriority, invalid := qos.GetOOMPriority(qosConfig, pod)
	if invalid {
		return 0, fmt.Errorf("pod %+v/%+v set invalid oom priority", pod.Namespace, pod.Name)
	}

	oomPriority := qos.AlignOOMPriority(qosLevel, userSpecifiedOOMPriority)

	return oomPriority, nil
}
