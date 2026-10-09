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

package qrm

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/intstr"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
)

type catWaysOperandKind int

const (
	catWaysOperandLiteral catWaysOperandKind = iota
	catWaysOperandMaxCATWays
	catWaysOperandMinCATWays
)

type catWaysOperator string

const (
	catWaysOperatorNone catWaysOperator = ""
	catWaysOperatorAdd  catWaysOperator = "+"
	catWaysOperatorSub  catWaysOperator = "-"
)

type catWaysOperand struct {
	kind  catWaysOperandKind
	value int64
	raw   string
}

// CATWaysExpression is a parsed CAT way count expression.
type CATWaysExpression struct {
	configured bool
	left       catWaysOperand
	operator   catWaysOperator
	right      catWaysOperand
}

// DeepCopy preserves the parsed expression across reflective configuration copies.
func (e CATWaysExpression) DeepCopy() interface{} {
	return e
}

// ParseCATWaysExpression parses a CAT way count expression.
func ParseCATWaysExpression(raw string) (CATWaysExpression, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return CATWaysExpression{}, fmt.Errorf("cat ways expression must not be empty")
	}

	var operator catWaysOperator
	var parts []string
	switch {
	case strings.Count(normalized, string(catWaysOperatorAdd)) == 1 && strings.Count(normalized, string(catWaysOperatorSub)) == 0:
		operator = catWaysOperatorAdd
		parts = strings.Split(normalized, string(catWaysOperatorAdd))
	case strings.Count(normalized, string(catWaysOperatorSub)) == 1 && strings.Count(normalized, string(catWaysOperatorAdd)) == 0:
		operator = catWaysOperatorSub
		parts = strings.Split(normalized, string(catWaysOperatorSub))
	case strings.Count(normalized, string(catWaysOperatorAdd)) == 0 && strings.Count(normalized, string(catWaysOperatorSub)) == 0:
		operator = catWaysOperatorNone
		parts = []string{normalized}
	default:
		return CATWaysExpression{}, fmt.Errorf("cat ways expression %q must contain at most one operator", raw)
	}

	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return CATWaysExpression{}, fmt.Errorf("cat ways expression %q has an empty operand", raw)
	}

	left, err := parseCATWaysOperand(strings.TrimSpace(parts[0]))
	if err != nil {
		return CATWaysExpression{}, err
	}
	expr := CATWaysExpression{
		configured: true,
		left:       left,
		operator:   operator,
	}

	if operator == catWaysOperatorNone {
		return expr, nil
	}
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return CATWaysExpression{}, fmt.Errorf("cat ways expression %q has an empty operand", raw)
	}
	right, err := parseCATWaysOperand(strings.TrimSpace(parts[1]))
	if err != nil {
		return CATWaysExpression{}, err
	}
	if operator == catWaysOperatorSub {
		switch {
		case left.kind == catWaysOperandLiteral && right.kind == catWaysOperandLiteral:
			return CATWaysExpression{}, fmt.Errorf("cat ways expression %q must simplify literal arithmetic", raw)
		case left.kind == right.kind:
			return CATWaysExpression{}, fmt.Errorf("cat ways expression %q always evaluates to zero", raw)
		case left.kind == catWaysOperandMinCATWays && right.kind == catWaysOperandMaxCATWays:
			return CATWaysExpression{}, fmt.Errorf("cat ways expression %q cannot evaluate to a positive value", raw)
		}
	}
	if operator == catWaysOperatorAdd &&
		left.kind == catWaysOperandLiteral && right.kind == catWaysOperandLiteral {
		return CATWaysExpression{}, fmt.Errorf("cat ways expression %q must simplify literal arithmetic", raw)
	}
	expr.right = right
	return expr, nil
}

// ParseCATWaysExpressionFromIntOrString parses an API IntOrString CAT way expression.
func ParseCATWaysExpressionFromIntOrString(value intstr.IntOrString) (CATWaysExpression, error) {
	switch value.Type {
	case intstr.Int:
		return ParseCATWaysExpression(strconv.Itoa(value.IntValue()))
	case intstr.String:
		return ParseCATWaysExpression(value.StrVal)
	default:
		return CATWaysExpression{}, fmt.Errorf("cat ways expression has unknown int-or-string type %d", value.Type)
	}
}

// Configured returns whether the expression was explicitly configured.
func (e CATWaysExpression) Configured() bool {
	return e.configured
}

// String returns the canonical expression string.
func (e CATWaysExpression) String() string {
	if !e.configured {
		return ""
	}
	if e.operator == catWaysOperatorNone {
		return e.left.String()
	}
	return e.left.String() + string(e.operator) + e.right.String()
}

// Evaluate returns the way count for a domain.
func (e CATWaysExpression) Evaluate(maxCATWays int64, minCATWays int64) (int64, error) {
	if !e.configured {
		return 0, fmt.Errorf("cat ways expression is not configured")
	}

	left, err := e.left.Evaluate(maxCATWays, minCATWays)
	if err != nil {
		return 0, err
	}

	result := left
	switch e.operator {
	case catWaysOperatorNone:
	case catWaysOperatorAdd:
		right, err := e.right.Evaluate(maxCATWays, minCATWays)
		if err != nil {
			return 0, err
		}
		result += right
	case catWaysOperatorSub:
		right, err := e.right.Evaluate(maxCATWays, minCATWays)
		if err != nil {
			return 0, err
		}
		result -= right
	default:
		return 0, fmt.Errorf("cat ways expression %q has unknown operator %q", e.String(), e.operator)
	}

	if result < 0 {
		return 0, fmt.Errorf("cat ways expression %q evaluated to %d, must be non-negative", e.String(), result)
	}
	return result, nil
}

func parseCATWaysOperand(raw string) (catWaysOperand, error) {
	switch raw {
	case string(configv1alpha1.CATWaysExpressionVariableMaxCATWays):
		return catWaysOperand{kind: catWaysOperandMaxCATWays, raw: raw}, nil
	case string(configv1alpha1.CATWaysExpressionVariableMinCATWays):
		return catWaysOperand{kind: catWaysOperandMinCATWays, raw: raw}, nil
	}

	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return catWaysOperand{}, fmt.Errorf("cat ways expression operand %q is invalid", raw)
	}
	if value <= 0 {
		return catWaysOperand{}, fmt.Errorf("cat ways expression operand %q must be positive", raw)
	}
	return catWaysOperand{kind: catWaysOperandLiteral, value: value, raw: strconv.FormatInt(value, 10)}, nil
}

func (o catWaysOperand) String() string {
	switch o.kind {
	case catWaysOperandLiteral:
		return strconv.FormatInt(o.value, 10)
	case catWaysOperandMaxCATWays, catWaysOperandMinCATWays:
		return o.raw
	default:
		return ""
	}
}

func (o catWaysOperand) Evaluate(maxCATWays int64, minCATWays int64) (int64, error) {
	switch o.kind {
	case catWaysOperandLiteral:
		return o.value, nil
	case catWaysOperandMaxCATWays:
		return maxCATWays, nil
	case catWaysOperandMinCATWays:
		return minCATWays, nil
	default:
		return 0, fmt.Errorf("cat ways expression operand is invalid")
	}
}
