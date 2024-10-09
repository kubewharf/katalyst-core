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

package mbw

import (
	"context"
	"testing"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/mbw/sampling"
)

type testKey string

type mockSampler struct {
	sampling.Sampler
	calledStartup bool
	calledSample  bool
}

func (m *mockSampler) Startup(ctx context.Context) error {
	m.calledStartup = true

	switch ctx.Value(testKey("case")) {
	case "ok":
		return nil
	case "ng":
		return errors.New("test error")
	default:
		return errors.New("unknown case")
	}
}

func (m *mockSampler) Sample(context.Context) {
	m.calledSample = true
}

func TestMBWMetricsProvisioner_Run(t *testing.T) {
	t.Parallel()
	mockSampler := &mockSampler{}

	m := MBWMetricsProvisioner{
		sampler: mockSampler,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, testKey("case"), "ok")
	m.Run(ctx)

	if !mockSampler.calledStartup {
		t.Errorf("expected sampler startup called; got %v", mockSampler.calledStartup)
	}
	if !mockSampler.calledSample {
		t.Errorf("expected sampler sample called; got %v", mockSampler.calledSample)
	}
}

func TestMBWMetricsProvisioner_Run_return_on_startup_error(t *testing.T) {
	t.Parallel()
	mockSampler := &mockSampler{}

	m := MBWMetricsProvisioner{
		sampler: mockSampler,
	}

	ctx := context.TODO()
	ctx = context.WithValue(ctx, testKey("case"), "ng")
	m.Run(ctx)

	if !mockSampler.calledStartup {
		t.Errorf("expected sampler startup called; got %v", mockSampler.calledStartup)
	}
	if mockSampler.calledSample {
		t.Errorf("expected sampler sample not called; got %v", mockSampler.calledSample)
	}

	// 2nd run
	m.Run(context.TODO())
	if mockSampler.calledSample {
		t.Errorf("expected sampler sample not called; got %v", mockSampler.calledSample)
	}
}
