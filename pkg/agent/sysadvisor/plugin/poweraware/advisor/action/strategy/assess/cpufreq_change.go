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
	"time"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/reader"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// minMHZ is the minimum mhz for a valid cpu freq
	minMHZ = 800
	// minElapsedTimeInitFreq is the time at least one newer sample has been collected
	minElapsedTimeInitFreq = 10 * time.Second
)

type cpuFreqChangeAssessor struct {
	initFreqMhz    int
	timestampClear time.Time
	cpuFreqReader  reader.MetricReader
}

func (c *cpuFreqChangeAssessor) Clear() {
	// 0 is uninitialized cpu freq; hopefully next read of cpu freq will populate it given adequate time
	c.initFreqMhz = 0
	c.timestampClear = time.Now()
}

func (c *cpuFreqChangeAssessor) AssessEffect(_ int) (int, error) {
	currentFreq, err := c.cpuFreqReader.Get(context.Background())
	if err != nil {
		return 0, errors.Wrap(err, "failed to fetch latest cpu freq to access dvfs effect")
	}

	return c.assessEffectByFreq(currentFreq)
}

func (c *cpuFreqChangeAssessor) assessEffectByFreq(currentFreq int) (int, error) {
	if c.initFreqMhz < minMHZ {
		return 0, fmt.Errorf("invalid initial frequency %d mhz", c.initFreqMhz)
	}

	if currentFreq < minMHZ {
		return 0, fmt.Errorf("invalid currentFreq frequency %d mhz", currentFreq)
	}

	if currentFreq >= c.initFreqMhz {
		return 0, nil
	}

	return 100 - currentFreq*100/c.initFreqMhz, nil
}

func (c *cpuFreqChangeAssessor) Update(_ int) {
	// no need to keep track of the cpu freq change as long as initial frequency is valid
	if c.initFreqMhz >= minMHZ {
		return
	}

	currentFreq, err := c.cpuFreqReader.Get(context.Background())
	if err != nil {
		general.Errorf("pap: failed to fetch latest cpu freq to populate initial freq: %v", err)
		return
	}

	if time.Now().Before(c.timestampClear.Add(minElapsedTimeInitFreq)) {
		general.Warningf("pap: too soon to populate the initial cpu freq")
		return
	}

	c.initFreqMhz = currentFreq
}

func (c *cpuFreqChangeAssessor) AssessTarget(actualWatt, desiredWatt int, maxDecreasePercent int) int {
	// keep as is if there is no room to decrease
	if maxDecreasePercent <= 0 {
		return actualWatt
	}

	// when there is decrease room for cpu frequency, lower the power in smaller portion to avoid the misstep
	lowerLimit := (100 - maxDecreasePercent/2) * actualWatt / 100
	if lowerLimit > desiredWatt {
		return lowerLimit
	}

	return desiredWatt
}

func NewCPUFreqChangeAssessor(initMhz int, nodeMetricGetter reader.NodeMetricGetter) Assessor {
	return &cpuFreqChangeAssessor{
		initFreqMhz:   initMhz,
		cpuFreqReader: reader.NewCPUFreqReader(nodeMetricGetter),
	}
}
