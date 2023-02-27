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

	"k8s.io/klog/v2"
)

var healthzCheckRules sync.Map

// HealthzCheckName describes which rule name for this check
type HealthzCheckName string

// HealthzCheckState describes the checking results
type HealthzCheckState string

type HealthzCheckResponse struct {
	State   HealthzCheckState `json:"state"`
	Message string            `json:"message"`
}

const (
	HealthzCheckStateReady    HealthzCheckState = "Ready"
	HealthzCheckStateNotReady HealthzCheckState = "NotReady"
	HealthzCheckStateUnknown  HealthzCheckState = "Unknown"
	HealthzCheckStateFailed   HealthzCheckState = "Failed"
)

// HealthzCheckFunc defined as a common function to define whether the corresponding component is healthy.
type HealthzCheckFunc func() (HealthzCheckResponse, error)

// RegisterHealthzCheckRules supports to register healthz check functions.
func RegisterHealthzCheckRules(name HealthzCheckName, f HealthzCheckFunc) {
	healthzCheckRules.Store(name, f)
}

func getRegisterReadinessCheckRules() map[HealthzCheckName]HealthzCheckFunc {
	rules := make(map[HealthzCheckName]HealthzCheckFunc)
	healthzCheckRules.Range(func(key, value interface{}) bool {
		rules[key.(HealthzCheckName)] = value.(HealthzCheckFunc)
		return true
	})
	return rules
}

// CheckHealthz walks through the registered healthz functions to provide an insight about
// the running states of current process.
// if functions failed, returns HealthzCheckStateFailed as the returned state.
func CheckHealthz() map[HealthzCheckName]HealthzCheckResponse {
	rules := getRegisterReadinessCheckRules()
	results := make(map[HealthzCheckName]HealthzCheckResponse)

	wg := sync.WaitGroup{}
	wg.Add(1)
	for name := range rules {
		ruleName := name
		go func() {
			defer wg.Done()
			response, err := rules[ruleName]()
			if err != nil {
				message := fmt.Sprintf("failed to perform healthz check for %v: %v", ruleName, err)
				klog.Errorf(message)

				response.State = HealthzCheckStateFailed
				response.Message = message
			}
			results[ruleName] = response
		}()
	}
	wg.Wait()
	return results
}
