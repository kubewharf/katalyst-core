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

package fetcher

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const reporterFetcherSuccessTimeout = time.Minute * 5

const (
	reporterFetcherRulesSyncLoop = "SyncLoop"
)

var reporterFetcherRules = sets.NewString(
	reporterFetcherRulesSyncLoop,
)

// healthzSyncLoop is easy, but other healthz check may be complicated,
// such.as. the dependency is crashed, the state-file is crashed to case
// the calculation logic can't work as expected, and so on.
func (m *ReporterPluginManager) healthzSyncLoop() {
	m.healthzState.Store(reporterFetcherRulesSyncLoop, time.Now())
}

// healthz returns whether reporter manager is healthy by checking the
// healthy transition time for each rule is out-of-date.
//
// during starting period, the results may last unhealthy for a while, and
// the caller functions should handle this situation.
func (m *ReporterPluginManager) healthz() (general.HealthzCheckResponse, error) {
	response := general.HealthzCheckResponse{
		State: general.HealthzCheckStateReady,
	}

	now := time.Now()
	var unHealthy []string
	for name := range reporterFetcherRules {
		updatedTime, ok := m.healthzState.Load(name)
		if !ok || now.After(updatedTime.(time.Time).Add(reporterFetcherSuccessTimeout)) {
			unHealthy = append(unHealthy, name)
		}
	}

	if len(unHealthy) != 0 {
		response.State = general.HealthzCheckStateNotReady
		response.Message = fmt.Sprintf("the following checks timeout: %s", strings.Join(unHealthy, ","))
	}

	return response, nil
}
