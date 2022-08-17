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

	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
)

type Stub struct {
	fields []*v1alpha1.ReportField
}

func NewReporterStub() *Stub {
	return &Stub{}
}

func (s *Stub) Update(ctx context.Context, fields []*v1alpha1.ReportField) error {
	s.fields = fields
	return nil
}

func (s *Stub) Run(ctx context.Context) {}

// GetReportedFields get current fields just for test
func (s *Stub) GetReportedFields() []*v1alpha1.ReportField {
	return s.fields
}
