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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestNewCappedSmoothWindow(t *testing.T) {
	t.Parallel()

	type args struct {
		minStep      resource.Quantity
		maxStep      resource.Quantity
		smoothWindow SmoothWindow
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "test-cpu",
			args: args{
				minStep:      resource.MustParse("0.3"),
				maxStep:      resource.MustParse("4"),
				smoothWindow: NewAggregatorSmoothWindow(SmoothWindowOpts{WindowSize: 5, TTL: 100 * time.Millisecond, UsedMillValue: true, AggregateFunc: SmoothWindowAggFuncAvg}),
			},
		},
		{
			name: "test-memory",
			args: args{
				minStep:      resource.MustParse("300Mi"),
				maxStep:      resource.MustParse("5Gi"),
				smoothWindow: NewAggregatorSmoothWindow(SmoothWindowOpts{WindowSize: 5, TTL: 100 * time.Millisecond, UsedMillValue: false, AggregateFunc: SmoothWindowAggFuncAvg}),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			NewCappedSmoothWindow(tt.args.minStep, tt.args.maxStep, tt.args.smoothWindow)
		})
	}
}

func TestCappedSmoothWindow_GetWindowedResources(t *testing.T) {
	t.Parallel()

	w := NewCappedSmoothWindow(
		resource.MustParse("0.3"),
		resource.MustParse("4"),
		NewAggregatorSmoothWindow(SmoothWindowOpts{WindowSize: 3, TTL: 100 * time.Millisecond, UsedMillValue: true, AggregateFunc: SmoothWindowAggFuncAvg}),
	)

	time.Sleep(10 * time.Millisecond)
	v := w.GetWindowedResources(resource.MustParse("0.6"))
	require.Nil(t, v)

	time.Sleep(10 * time.Millisecond)
	v = w.GetWindowedResources(resource.MustParse("0.8"))
	require.Nil(t, v)

	time.Sleep(10 * time.Millisecond)
	v = w.GetWindowedResources(resource.MustParse("0.4"))
	require.NotNil(t, v)
	require.Zero(t, v.Cmp(resource.MustParse("0.6")))

	time.Sleep(10 * time.Millisecond)
	v = w.GetWindowedResources(resource.MustParse("1.2"))
	require.NotNil(t, v)
	require.True(t, resource.MustParse("0.6").Equal(*v))

	time.Sleep(10 * time.Millisecond)
	v = w.GetWindowedResources(resource.MustParse("1.4"))
	require.NotNil(t, v)
	require.Equal(t, int64(1000), v.MilliValue())

	time.Sleep(10 * time.Millisecond)
	v = w.GetWindowedResources(resource.MustParse("15"))
	require.NotNil(t, v)
	require.Equal(t, int64(5000), v.MilliValue())

	time.Sleep(90 * time.Millisecond)
	v = w.GetWindowedResources(resource.MustParse("0"))
	require.NotNil(t, v)
	require.Equal(t, int64(5000), v.MilliValue())

	time.Sleep(10 * time.Millisecond)
	v = w.GetWindowedResources(resource.MustParse("0"))
	require.NotNil(t, v)
	require.Equal(t, int64(5000), v.MilliValue())

	time.Sleep(30 * time.Millisecond)
	v = w.GetWindowedResources(resource.MustParse("0"))
	require.NotNil(t, v)
	require.Equal(t, int64(1000), v.MilliValue())
}

func TestPercentileWithTTLSmoothWindow(t *testing.T) {
	t.Parallel()

	type args struct {
		windowSize int
		ttl        time.Duration
		percentile float64
		values     []resource.Quantity
	}
	tests := []struct {
		name       string
		args       args
		wantValues []resource.Quantity
	}{
		{
			name: "p100(max)",
			args: args{
				windowSize: 5,
				ttl:        1 * time.Second,
				percentile: 100,
				values: []resource.Quantity{
					resource.MustParse("1"),
					resource.MustParse("2"),
					resource.MustParse("1.5"),
					resource.MustParse("3"),
					resource.MustParse("2"),
					resource.MustParse("6"),
					resource.MustParse("5"),
					resource.MustParse("1"),
					resource.MustParse("1"),
					resource.MustParse("1"),
					resource.MustParse("1"),
					resource.MustParse("1"),
				},
			},
			wantValues: []resource.Quantity{
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("3"),
				resource.MustParse("6"),
				resource.MustParse("6"),
				resource.MustParse("6"),
				resource.MustParse("6"),
				resource.MustParse("6"),
				resource.MustParse("5"),
				resource.MustParse("1"),
			},
		},
		{
			name: "p0(min)",
			args: args{
				windowSize: 5,
				ttl:        1 * time.Second,
				percentile: 0.0,
				values: []resource.Quantity{
					resource.MustParse("1"),
					resource.MustParse("2"),
					resource.MustParse("1.5"),
					resource.MustParse("3"),
					resource.MustParse("2"),
					resource.MustParse("6"),
					resource.MustParse("5"),
					resource.MustParse("1"),
					resource.MustParse("1"),
					resource.MustParse("1"),
					resource.MustParse("1"),
					resource.MustParse("1"),
				},
			},
			wantValues: []resource.Quantity{
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("1"),
				resource.MustParse("1.5"),
				resource.MustParse("1.5"),
				resource.MustParse("1"),
				resource.MustParse("1"),
				resource.MustParse("1"),
				resource.MustParse("1"),
				resource.MustParse("1"),
			},
		},
		{
			name: "p20",
			args: args{
				windowSize: 10,
				ttl:        111111 * time.Second,
				percentile: 20,
				values: []resource.Quantity{
					resource.MustParse("1"),
					resource.MustParse("2"),
					resource.MustParse("3"),
					resource.MustParse("4"),
					resource.MustParse("5"),
					resource.MustParse("6"),
					resource.MustParse("7"),
					resource.MustParse("8"),
					resource.MustParse("9"),
					resource.MustParse("10"),
					resource.MustParse("11"),
					resource.MustParse("12"),
				},
			},
			wantValues: []resource.Quantity{
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("0"),
				resource.MustParse("2"),
				resource.MustParse("3"),
				resource.MustParse("4"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewPercentileWithTTLSmoothWindow(tt.args.windowSize, tt.args.ttl, tt.args.percentile, true)
			for i, v := range tt.args.values {
				ret := w.GetWindowedResources(v)
				if ret == nil {
					require.Equal(t, tt.wantValues[i].MilliValue(), int64(0))
				} else {
					require.Equal(t, tt.wantValues[i].MilliValue(), ret.MilliValue())
				}
			}
		})
	}
}

func TestWindowEmpty(t *testing.T) {
	t.Parallel()

	type args struct {
		windowSize int
		ttl        time.Duration
		percentile float64
		samples    []*sample
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "empty",
			args: args{
				windowSize: 10,
				ttl:        111111 * time.Second,
				percentile: 20,
				samples:    []*sample{},
			},
			want: true,
		},
		{
			name: "samples all expired",
			args: args{
				windowSize: 10,
				ttl:        111111 * time.Second,
				percentile: 20,
				samples: []*sample{
					{
						resource.MustParse("1"),
						time.Date(2000, 0, 0, 0, 0, 0, 0, time.UTC),
					},
				},
			},
			want: true,
		},
		{
			name: "have valid sample",
			args: args{
				windowSize: 10,
				ttl:        111111 * time.Second,
				percentile: 20,
				samples: []*sample{
					{
						resource.MustParse("1"),
						time.Now(),
					},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewPercentileWithTTLSmoothWindow(tt.args.windowSize, tt.args.ttl, tt.args.percentile, true)
			window := w.(*percentileWithTTLSmoothWindow)
			window.samples = tt.args.samples

			assert.Equal(t, tt.want, window.Empty())
			window.Empty()
		})
	}
}
