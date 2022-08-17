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

package process

import (
	"sync"
	"time"
)

const (
	endpointStopGracePeriod = time.Duration(5) * time.Minute
)

type StopControl struct {
	stopTime time.Time
	sync.RWMutex
}

func (sc *StopControl) Stop() {
	if sc == nil {
		return
	}

	sc.Lock()
	defer sc.Unlock()
	sc.stopTime = time.Now()
}

func (sc *StopControl) IsStopped() bool {
	if sc == nil {
		return true
	}

	sc.RLock()
	defer sc.RUnlock()
	return !sc.stopTime.IsZero()
}

func (sc *StopControl) StopGracePeriodExpired() bool {
	if sc == nil {
		return true
	}

	sc.RLock()
	defer sc.RUnlock()
	return !sc.stopTime.IsZero() && time.Since(sc.stopTime) > endpointStopGracePeriod
}

func NewStopControl(stopTime time.Time) *StopControl {
	return &StopControl{
		stopTime: stopTime,
	}
}
