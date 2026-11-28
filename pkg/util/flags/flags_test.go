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

package flags

import (
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func TestExplicitValue(t *testing.T) {
	t.Parallel()

	t.Run("zero value", func(t *testing.T) {
		t.Parallel()

		var value ExplicitValue[int64]
		if value.Value != 0 || value.Changed() {
			t.Fatalf("zero ExplicitValue = (%d, %v), want (0, false)",
				value.Value, value.Changed())
		}
	})

	t.Run("string value", func(t *testing.T) {
		t.Parallel()

		var value ExplicitValue[string]
		fs := pflag.NewFlagSet("string", pflag.ContinueOnError)
		fs.StringVar(&value.Value, "value", value.Value, "")
		value.TrackFlag(fs, "value")

		if err := fs.Parse([]string{"--value=explicit"}); err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		if value.Value != "explicit" || !value.Changed() {
			t.Fatalf("ExplicitValue = (%q, %v), want (%q, true)",
				value.Value, value.Changed(), "explicit")
		}
	})

	t.Run("single flag set", func(t *testing.T) {
		t.Parallel()

		var value ExplicitValue[int64]
		fs := pflag.NewFlagSet("single", pflag.ContinueOnError)
		fs.Int64Var(&value.Value, "value", value.Value, "")
		value.TrackFlag(fs, "value")

		if value.Changed() {
			t.Fatal("Changed before Parse = true, want false")
		}
		if err := fs.Parse([]string{"--value=7"}); err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		if value.Value != 7 || !value.Changed() {
			t.Fatalf("ExplicitValue = (%d, %v), want (7, true)",
				value.Value, value.Changed())
		}
	})

	t.Run("multiple flag sets report any change", func(t *testing.T) {
		t.Parallel()

		var value ExplicitValue[int64]
		first := pflag.NewFlagSet("first", pflag.ContinueOnError)
		second := pflag.NewFlagSet("second", pflag.ContinueOnError)
		first.Int64Var(&value.Value, "value", value.Value, "")
		value.TrackFlag(first, "value")
		second.Int64Var(&value.Value, "value", value.Value, "")
		value.TrackFlag(second, "value")

		if err := second.Parse([]string{"--value=11"}); err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		if first.Lookup("value").Changed {
			t.Fatal("first flag Changed = true, want false")
		}
		if value.Value != 11 || !value.Changed() {
			t.Fatalf("ExplicitValue = (%d, %v), want (11, true)",
				value.Value, value.Changed())
		}
	})

	t.Run("unknown flag panics", func(t *testing.T) {
		t.Parallel()

		var value ExplicitValue[int64]
		fs := pflag.NewFlagSet("missing", pflag.ContinueOnError)

		defer func() {
			got := recover()
			const want = `flag "missing" is not registered`
			if got != want {
				t.Fatalf("panic = %#v, want %q", got, want)
			}
		}()
		value.TrackFlag(fs, "missing")
	})
}

func TestReservedMemoryVar(t *testing.T) {
	t.Parallel()

	resourceNameHugepages1Gi := v1.ResourceName(fmt.Sprintf("%s1Gi", v1.ResourceHugePagesPrefix))
	memory1Gi := resource.MustParse("1Gi")
	testCases := []struct {
		desc      string
		argc      string
		expectErr bool
		expectVal []native.MemoryReservation
	}{
		{
			desc: "valid input",
			argc: "blah --reserved-memory=0:memory=1Gi",
			expectVal: []native.MemoryReservation{
				{
					NumaNode: 0,
					Limits: v1.ResourceList{
						v1.ResourceMemory: memory1Gi,
					},
				},
			},
		},
		{
			desc: "valid input with multiple memory types",
			argc: "blah --reserved-memory=0:memory=1Gi,hugepages-1Gi=1Gi",
			expectVal: []native.MemoryReservation{
				{
					NumaNode: 0,
					Limits: v1.ResourceList{
						v1.ResourceMemory:        memory1Gi,
						resourceNameHugepages1Gi: memory1Gi,
					},
				},
			},
		},
		{
			desc: "valid input with multiple reserved-memory arguments",
			argc: "blah --reserved-memory=0:memory=1Gi,hugepages-1Gi=1Gi --reserved-memory=1:memory=1Gi",
			expectVal: []native.MemoryReservation{
				{
					NumaNode: 0,
					Limits: v1.ResourceList{
						v1.ResourceMemory:        memory1Gi,
						resourceNameHugepages1Gi: memory1Gi,
					},
				},
				{
					NumaNode: 1,
					Limits: v1.ResourceList{
						v1.ResourceMemory: memory1Gi,
					},
				},
			},
		},
		{
			desc: "valid input with '/' as separator for multiple reserved-memory arguments",
			argc: "blah --reserved-memory=0:memory=1Gi,hugepages-1Gi=1Gi/1:memory=1Gi",
			expectVal: []native.MemoryReservation{
				{
					NumaNode: 0,
					Limits: v1.ResourceList{
						v1.ResourceMemory:        memory1Gi,
						resourceNameHugepages1Gi: memory1Gi,
					},
				},
				{
					NumaNode: 1,
					Limits: v1.ResourceList{
						v1.ResourceMemory: memory1Gi,
					},
				},
			},
		},
		{
			desc:      "invalid input",
			argc:      "blah --reserved-memory=bad-input",
			expectVal: nil,
			expectErr: true,
		},
		{
			desc:      "invalid input without memory types",
			argc:      "blah --reserved-memory=0:",
			expectVal: nil,
			expectErr: true,
		},
		{
			desc:      "invalid input with non-integer NUMA node",
			argc:      "blah --reserved-memory=a:memory=1Gi",
			expectVal: nil,
			expectErr: true,
		},
		{
			desc:      "invalid input with invalid limit",
			argc:      "blah --reserved-memory=0:memory=",
			expectVal: nil,
			expectErr: true,
		},
		{
			desc:      "invalid input with invalid memory type",
			argc:      "blah --reserved-memory=0:type=1Gi",
			expectVal: nil,
			expectErr: true,
		},
		{
			desc:      "invalid input with invalid quantity",
			argc:      "blah --reserved-memory=0:memory=1Be",
			expectVal: nil,
			expectErr: true,
		},
	}
	for _, tc := range testCases {
		tc := tc
		fs := pflag.NewFlagSet("blah", pflag.PanicOnError)

		var reservedMemory []native.MemoryReservation
		fs.Var(&ReservedMemoryVar{Value: &reservedMemory}, "reserved-memory", "--reserved-memory 0:memory=1Gi,hugepages-1M=2Gi")

		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = r.(error)
				}
			}()
			fs.Parse(strings.Split(tc.argc, " "))
		}()

		if tc.expectErr && err == nil {
			t.Fatalf("%q: Did not observe an expected error", tc.desc)
		}
		if !tc.expectErr && err != nil {
			t.Fatalf("%q: Observed an unexpected error: %v", tc.desc, err)
		}
		if !apiequality.Semantic.DeepEqual(reservedMemory, tc.expectVal) {
			t.Fatalf("%q: Unexpected reserved-error: expected %v, saw %v", tc.desc, tc.expectVal, reservedMemory)
		}
	}
}
