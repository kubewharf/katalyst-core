/*
Copyright 2026 The Katalyst Authors.

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

package qrm

import "testing"

func TestParseCATWaysExpression(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "literal", raw: "4", want: "4"},
		{name: "max cat ways", raw: "MaxCATWays", want: "MaxCATWays"},
		{name: "min cat ways", raw: "MinCATWays", want: "MinCATWays"},
		{name: "subtract variable", raw: "MaxCATWays - MinCATWays", want: "MaxCATWays-MinCATWays"},
		{name: "subtract literal", raw: "MaxCATWays-2", want: "MaxCATWays-2"},
		{name: "add literal", raw: "MinCATWays+1", want: "MinCATWays+1"},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCATWaysExpression(tt.raw)
			if err != nil {
				t.Fatalf("ParseCATWaysExpression(%q) error = %v", tt.raw, err)
			}
			if got.String() != tt.want {
				t.Fatalf("ParseCATWaysExpression(%q) = %s, want %s", tt.raw, got.String(), tt.want)
			}
			if !got.Configured() {
				t.Fatalf("ParseCATWaysExpression(%q) is not configured", tt.raw)
			}
		})
	}
}

func TestParseCATWaysExpressionInvalid(t *testing.T) {
	t.Parallel()

	cases := []string{
		"",
		" ",
		"0",
		"-1",
		"CBMMask",
		"MinCBMBits",
		"CBMMask-MinCBMBits",
		"MaxCATWays-CBMMask",
		"MinCBMBits+1",
		"CBMMask/2",
		"MaxCATWays-MinCATWays-1",
		"cbm_mask",
		"MaxCATWays+0",
		"1 2",
		"C B M Mask",
		"Min CATWays+1",
		"1-2",
		"1+2",
		"MaxCATWays-MaxCATWays",
		"MinCATWays-MinCATWays",
		"MinCATWays-MaxCATWays",
	}

	for _, raw := range cases {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := ParseCATWaysExpression(raw); err == nil {
				t.Fatalf("ParseCATWaysExpression(%q) = %s, want error", raw, got.String())
			}
		})
	}
}

func TestCATWaysExpressionEvaluate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		raw        string
		maxCATWays int64
		minCATWays int64
		want       int64
	}{
		{name: "literal", raw: "4", maxCATWays: 11, minCATWays: 2, want: 4},
		{name: "max cat ways", raw: "MaxCATWays", maxCATWays: 11, minCATWays: 2, want: 11},
		{name: "min cat ways", raw: "MinCATWays", maxCATWays: 11, minCATWays: 2, want: 2},
		{name: "zero min cat ways", raw: "MinCATWays", maxCATWays: 16, minCATWays: 0, want: 0},
		{name: "subtract variable", raw: "MaxCATWays-MinCATWays", maxCATWays: 11, minCATWays: 2, want: 9},
		{name: "subtract literal", raw: "MaxCATWays-2", maxCATWays: 11, minCATWays: 2, want: 9},
		{name: "add literal", raw: "MinCATWays+1", maxCATWays: 11, minCATWays: 2, want: 3},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			expr, err := ParseCATWaysExpression(tt.raw)
			if err != nil {
				t.Fatalf("ParseCATWaysExpression(%q) error = %v", tt.raw, err)
			}
			got, err := expr.Evaluate(tt.maxCATWays, tt.minCATWays)
			if err != nil {
				t.Fatalf("Evaluate(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("Evaluate(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

func TestCATWaysExpressionEvaluateInvalidRuntimeInputs(t *testing.T) {
	t.Parallel()

	expr, err := ParseCATWaysExpression("MaxCATWays-MinCATWays")
	if err != nil {
		t.Fatalf("ParseCATWaysExpression error = %v", err)
	}
	if _, err := expr.Evaluate(1, 2); err == nil {
		t.Fatal("Evaluate with negative result succeeded, want error")
	}
	if _, err := (CATWaysExpression{}).Evaluate(11, 2); err == nil {
		t.Fatal("Evaluate unconfigured expression succeeded, want error")
	}
}
