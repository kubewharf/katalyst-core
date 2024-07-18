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

package prom

import (
	"context"
	"fmt"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
)

type FakeProvider struct {
	ts []*common.TimeSeries
}

func NewFakeProvider(timeSeries []*common.TimeSeries) *FakeProvider {
	return &FakeProvider{
		ts: timeSeries,
	}
}

func (f *FakeProvider) QueryTimeSeries(_ context.Context, _ string, _, _ time.Time, _ time.Duration) ([]*common.TimeSeries, error) {
	if f.ts == nil {
		return nil, fmt.Errorf("test error")
	}
	return f.ts, nil
}

func (f *FakeProvider) BuildQuery(_ string, _ []common.Metadata) (string, error) {
	return "", nil
}
