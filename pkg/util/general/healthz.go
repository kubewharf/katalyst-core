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

package general

import (
	"fmt"
	"sync"
	"time"
)

var (
	healthzCheckMap  = make(map[HealthzCheckName]*healthzCheckStatus)
	healthzCheckLock sync.RWMutex
)

// HealthzCheckName describes which rule name for this check
type HealthzCheckName string

// HealthzCheckState describes the checking results
type HealthzCheckState string

type HealthzCheckMode string

type HealthzCheckResult struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message"`
}

type healthzCheckStatus struct {
	State          HealthzCheckState `json:"state"`
	Message        string            `json:"message"`
	LastUpdateTime time.Time         `json:"lastUpdateTime"`

	Mode HealthzCheckMode `json:"mode"`

	// in HealthzCheckModeHeartBeat mode, when LastUpdateTime is not updated for more than TimeoutPeriod, we consider this rule is failed.
	// 0 or negative value means no need to check the LastUpdateTime.
	TimeoutPeriod      time.Duration `json:"timeoutPeriod"`
	UnhealthyStartTime time.Time     `json:"unhealthyStartTime"`
	// in HealthzCheckModeHeartBeat mode, when current State is not HealthzCheckStateReady, and it lasts more than
	// TolerationPeriod, we consider this rule is failed. 0 or negative value means no need to check the UnhealthyStartTime.
	TolerationPeriod time.Duration `json:"gracePeriod"`

	LatestUnhealthyTime time.Time `json:"latestUnhealthyTime"`
	// in HealthzCheckModeReport mode, when LatestUnhealthyTime is not earlier than AutoRecoverPeriod ago, we consider this rule
	// is failed.
	AutoRecoverPeriod time.Duration `json:"autoRecoverPeriod"`
	mutex             sync.RWMutex
}

func (h *healthzCheckStatus) update(state HealthzCheckState, message string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	now := time.Now()
	h.Message = message
	h.LastUpdateTime = now
	if h.State == HealthzCheckStateReady && state != HealthzCheckStateReady {
		h.UnhealthyStartTime = now
	}
	if state != HealthzCheckStateReady {
		h.LatestUnhealthyTime = now
	}
	h.State = state
}

const (
	HealthzCheckStateReady    HealthzCheckState = "Ready"
	HealthzCheckStateNotReady HealthzCheckState = "NotReady"
	HealthzCheckStateUnknown  HealthzCheckState = "Unknown"
	HealthzCheckStateFailed   HealthzCheckState = "Failed"

	InitMessage = "Init"

	// HealthzCheckModeHeartBeat in this mode, caller should update the check status regularly like a heartbeat, once
	// the heartbeat stops for more than TimeoutPeriod or the state is not HealthzCheckStateReady for more than GracePeriod,
	// this rule will be considered as unhealthy.
	HealthzCheckModeHeartBeat HealthzCheckMode = "heartbeat"
	// HealthzCheckModeReport in this mode, caller only reports the failed state when the function does not work well.
	// when the LatestUnhealthyTime is not earlier than the GracePeriod ago, we consider this rule as unhealthy.
	// if caller doesn't report new failed state for more than GracePeriod, we consider the exception recovered.
	HealthzCheckModeReport HealthzCheckMode = "report"
)

// HealthzCheckFunc defined as a common function to define whether the corresponding component is healthy.
type HealthzCheckFunc func() (healthzCheckStatus, error)

func RegisterHeartbeatCheck(name string, timeout time.Duration, initState HealthzCheckState, tolerationPeriod time.Duration) {
	healthzCheckLock.Lock()
	defer healthzCheckLock.Unlock()

	healthzCheckMap[HealthzCheckName(name)] = &healthzCheckStatus{
		State:            initState,
		Message:          InitMessage,
		LastUpdateTime:   time.Now(),
		TimeoutPeriod:    timeout,
		TolerationPeriod: tolerationPeriod,
		Mode:             HealthzCheckModeHeartBeat,
	}
}

func RegisterReportCheck(name string, autoRecoverPeriod time.Duration) {
	healthzCheckLock.Lock()
	defer healthzCheckLock.Unlock()

	healthzCheckMap[HealthzCheckName(name)] = &healthzCheckStatus{
		State:             HealthzCheckStateReady,
		Message:           InitMessage,
		AutoRecoverPeriod: autoRecoverPeriod,
		Mode:              HealthzCheckModeReport,
	}
}

func UpdateHealthzStateByError(name string, err error) error {
	if err != nil {
		return UpdateHealthzState(name, HealthzCheckStateNotReady, err.Error())
	} else {
		return UpdateHealthzState(name, HealthzCheckStateReady, "")
	}
}

func UpdateHealthzState(name string, state HealthzCheckState, message string) error {
	healthzCheckLock.RLock()
	defer healthzCheckLock.RUnlock()

	status, ok := healthzCheckMap[HealthzCheckName(name)]
	if !ok {
		Errorf("check rule %v not found", name)
		return fmt.Errorf("check rule %v not found", name)
	}
	status.update(state, message)
	return nil
}

func GetRegisterReadinessCheckResult() map[HealthzCheckName]HealthzCheckResult {
	healthzCheckLock.RLock()
	defer healthzCheckLock.RUnlock()

	results := make(map[HealthzCheckName]HealthzCheckResult)
	for name, checkStatus := range healthzCheckMap {
		func() {
			checkStatus.mutex.RLock()
			defer checkStatus.mutex.RUnlock()

			ready := true
			message := checkStatus.Message
			switch checkStatus.Mode {
			case HealthzCheckModeHeartBeat:
				if checkStatus.TimeoutPeriod > 0 && time.Now().Sub(checkStatus.LastUpdateTime) > checkStatus.TimeoutPeriod {
					ready = false
					message = fmt.Sprintf("the status has not been updated for more than %v, last update time is %v", checkStatus.TimeoutPeriod, checkStatus.LastUpdateTime)
				}

				if checkStatus.TolerationPeriod <= 0 && checkStatus.State != HealthzCheckStateReady {
					ready = false
				}

				if checkStatus.TolerationPeriod > 0 && time.Now().Sub(checkStatus.UnhealthyStartTime) > checkStatus.TolerationPeriod &&
					checkStatus.State != HealthzCheckStateReady {
					ready = false
				}
			case HealthzCheckModeReport:
				if checkStatus.LatestUnhealthyTime.After(time.Now().Add(-checkStatus.TolerationPeriod)) {
					ready = false
				}
			}
			results[name] = HealthzCheckResult{
				Ready:   ready,
				Message: message,
			}
		}()
	}
	return results
}
