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
				smoothWindow: NewAverageWithTTLSmoothWindow(5, 100*time.Millisecond, true),
			},
		},
		{
			name: "test-memory",
			args: args{
				minStep:      resource.MustParse("300Mi"),
				maxStep:      resource.MustParse("5Gi"),
				smoothWindow: NewAverageWithTTLSmoothWindow(5, 100*time.Millisecond, false),
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
		NewAverageWithTTLSmoothWindow(3, 100*time.Millisecond, true),
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
