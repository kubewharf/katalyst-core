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

package reporter

import (
	"context"
	"sync"

	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
)

// ManagerStub a reporter manager stub
type ManagerStub struct {
	mutex                 sync.RWMutex
	reportContentResponse map[string]*v1alpha1.GetReportContentResponse
}

// Run start the reporter manager stub
func (s *ManagerStub) Run(context.Context) {}

// NewReporterManagerStub new a reporter stub
func NewReporterManagerStub() *ManagerStub {
	return &ManagerStub{
		reportContentResponse: make(map[string]*v1alpha1.GetReportContentResponse),
	}
}

// PushContents store response to local cache
func (s *ManagerStub) PushContents(_ context.Context, responses map[string]*v1alpha1.GetReportContentResponse) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.reportContentResponse = responses
	return nil
}

// GetReportContentResponse get reporter content from local cache
func (s *ManagerStub) GetReportContentResponse(pluginName string) *v1alpha1.GetReportContentResponse {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.reportContentResponse[pluginName]
}
