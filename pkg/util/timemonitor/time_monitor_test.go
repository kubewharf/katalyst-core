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

package timemonitor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestTimeMonitor(t *testing.T) {
	t.Parallel()

	timeMonitor, err := NewTimeMonitor("test", time.Second, 1*time.Second, 5*time.Second, "test_metric_name", metrics.DummyMetrics{}, 3, true)
	as := require.New(t)

	as.Nil(err)

	healthy := timeMonitor.GetHealthy()
	as.True(healthy)

	time.Sleep(2 * time.Second)
	timeMonitor.monitor()
	healthy = timeMonitor.GetHealthy()
	as.False(healthy)

	timeMonitor.UpdateRefreshTime()
	timeMonitor.UpdateRefreshTime()
	timeMonitor.UpdateRefreshTime()
	timeMonitor.monitor()

	healthy = timeMonitor.GetHealthy()
	as.True(healthy)
}
