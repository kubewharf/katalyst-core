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

package assess

import (
	"context"
	"fmt"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/reader"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// minKHZ is the minimum khz for a valid cpu freq
	minKHZ = 1000_000
)

type cpuFreqChangeAssessor struct {
	// highFreqKHZ may update itself to higher value observed
	// todo: consider to get fixed base cpu freq value instead of this self-updated one
	highFreqKHZ int
	highEffect  int

	cpuFreqReader reader.MetricReader
}

func (c *cpuFreqChangeAssessor) IsInitialized() bool {
	return c.highFreqKHZ >= minKHZ
}

func (c *cpuFreqChangeAssessor) Init() error {
	freq, err := c.cpuFreqReader.Get(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to get initial cpu freq value")
	}

	general.Infof("pap: cpufreq set initial value %d khz", freq)
	c.highFreqKHZ = freq
	return nil
}

func (c *cpuFreqChangeAssessor) Clear() {
	c.highEffect = 0
}

func (c *cpuFreqChangeAssessor) AssessEffect(_ int, _, _ bool) (int, error) {
	// always check cpu freq to assess the effect
	currentFreq, err := c.cpuFreqReader.Get(context.Background())
	if err != nil {
		return 0, errors.Wrap(err, "failed to fetch latest cpu freq to access dvfs effect")
	}

	if currentFreq > c.highFreqKHZ {
		c.highFreqKHZ = currentFreq
		c.highEffect = 0
	}

	return c.assessEffectByFreq(currentFreq)
}

func (c *cpuFreqChangeAssessor) assessEffectByFreq(currentFreq int) (int, error) {
	general.InfofV(6, "pap: cpuFreqChangeAssessor assessEffectByFreq: curr %d, base %d", currentFreq, c.highFreqKHZ)
	if currentFreq < minKHZ {
		return 0, fmt.Errorf("invalid currentFreq frequency %d khz", currentFreq)
	}

	if currentFreq >= c.highFreqKHZ {
		return 0, nil
	}

	instantEffect := 100 - currentFreq*100/c.highFreqKHZ
	if instantEffect > c.highEffect {
		c.highEffect = instantEffect
	}
	return c.highEffect, nil
}

func (c *cpuFreqChangeAssessor) Update(_ int) {
	// no need to keep track of the cpu freq change as the initial cpu freq is always the baseline
}

func (c *cpuFreqChangeAssessor) AssessTarget(actualWatt, desiredWatt int, maxDecreasePercent int) int {
	// keep as is if there is no room to decrease
	if maxDecreasePercent <= 0 {
		return actualWatt
	}

	// when there is decrease room for cpu frequency, lower the power in smaller portion to avoid misstep
	lowerLimit := (100 - maxDecreasePercent/2) * actualWatt / 100
	if lowerLimit > desiredWatt {
		return lowerLimit
	}

	return desiredWatt
}

func NewCPUFreqChangeAssessor(initKHZ int, nodeMetricGetter reader.NodeMetricGetter) Assessor {
	return &cpuFreqChangeAssessor{
		highFreqKHZ:   initKHZ,
		cpuFreqReader: reader.NewCPUFreqReader(nodeMetricGetter),
	}
}
