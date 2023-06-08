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
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
)

func Max(a, b int) int {
	if a >= b {
		return a
	} else {
		return b
	}
}

func Min(a, b int) int {
	if a >= b {
		return b
	} else {
		return a
	}
}

func MaxUInt64(a, b uint64) uint64 {
	if a >= b {
		return a
	} else {
		return b
	}
}

func MinUInt64(a, b uint64) uint64 {
	if a <= b {
		return a
	} else {
		return b
	}
}

func MaxInt64(a, b int64) int64 {
	if a >= b {
		return a
	} else {
		return b
	}
}

// IsNameEnabled check if a specified name enabled or not.
func IsNameEnabled(name string, disabledByDefault sets.String, enableNames []string) bool {
	hasStar := false
	for _, ctrl := range enableNames {
		if ctrl == name {
			return true
		}
		if ctrl == "-"+name {
			return false
		}
		if ctrl == "*" {
			hasStar = true
		}
	}
	// if we get here, there was no explicit choice
	if !hasStar {
		// nothing on by default
		return false
	}

	if disabledByDefault != nil {
		return !disabledByDefault.Has(name)
	}
	return true
}

func ParseUint64PointerToString(v *uint64) string {
	if v == nil {
		return "nil"
	} else {
		return fmt.Sprintf("%d", *v)
	}
}

func ParseStringToUint64Pointer(s string) (*uint64, error) {
	if s == "nil" {
		return nil, nil
	} else {
		v, err := strconv.ParseUint(s, 10, 64)

		if err != nil {
			return nil, err
		}

		return &v, nil
	}
}

func GetInt64PointerFromUint64Pointer(v *uint64) (*int64, error) {
	if v == nil {
		return nil, nil
	}

	ret := int64(*v)

	if ret < 0 {
		return &ret, fmt.Errorf("transformation overflows")
	} else {
		return &ret, nil
	}
}

func GenerateHash(data []byte, length int) string {
	h := sha256.New()
	h.Write(data)
	result := fmt.Sprintf("%x", h.Sum(nil))
	if len(result) > length {
		return result[:length]
	}
	return result
}

func UIntPointerToFloat64(p *uint) float64 {
	if p == nil {
		return 0
	}
	return float64(*p)
}

func UInt64PointerToFloat64(p *uint64) float64 {
	if p == nil {
		return 0
	}
	return float64(*p)
}

// JsonPathEmpty is used to check whether the given str is empty for json-patch
func JsonPathEmpty(str []byte) bool {
	if "{}" == string(str) || "" == string(str) {
		return true
	}
	return false
}

// GetValueWithDefault gets value from the given map, and returns default if key not exist
func GetValueWithDefault(m map[string]string, key, defaultV string) string {
	if _, ok := m[key]; !ok {
		return defaultV
	}
	return m[key]
}

func GetStringValueFromMap(m map[string]string, key string) string {
	if value, found := m[key]; found {
		return value
	}
	return ""
}

// SumUpMultipleMapValues accumulates total values for the given multi-level map
func SumUpMultipleMapValues(m map[string]map[string]int) int {
	total := 0
	for _, v := range m {
		total += SumUpMapValues(v)
	}
	return total
}

// SumUpMapValues accumulates total values for the given map
func SumUpMapValues(m map[string]int) int {
	total := 0
	for _, quantity := range m {
		total += quantity
	}
	return total
}

func CheckMapEqual(pre, cur map[string]string) bool {
	if len(pre) != len(cur) {
		return false
	}

	for key, value := range pre {
		if value != cur[key] {
			return false
		}
	}
	return true
}

// MergeMap merges the contents from override into the src
func MergeMap(src, override map[string]string) map[string]string {
	res := map[string]string{}
	for k, v := range src {
		res[k] = v
	}
	for k, v := range override {
		res[k] = v
	}
	return res
}

// MergeMapInt merges the contents from override into the src
func MergeMapInt(src, override map[string]int) map[string]int {
	res := map[string]int{}
	for k, v := range src {
		res[k] = v
	}
	for k, v := range override {
		res[k] = v
	}
	return res
}

// GetSortedMapKeys returns a slice containing sorted keys for the given map
func GetSortedMapKeys(m map[string]int) []string {
	ret := make([]string, 0, len(m))
	for key := range m {
		ret = append(ret, key)
	}
	sort.Strings(ret)
	return ret
}

// ParseMapWithPrefix converts selector string to label map
// and validates keys and values
func ParseMapWithPrefix(prefix, selector string) (map[string]string, error) {
	labelsMap := make(map[string]string)

	if len(selector) == 0 {
		return labelsMap, nil
	}

	labels := strings.Split(selector, ",")
	for _, label := range labels {
		l := strings.Split(label, "=")
		if len(l) != 2 {
			return labelsMap, fmt.Errorf("invalid selector: %s", l)
		}

		key := strings.TrimSpace(l[0])
		value := strings.TrimSpace(l[1])
		labelsMap[prefix+key] = value
	}
	return labelsMap, nil
}

// ToString transform to string for better display etc. in log
func ToString(in interface{}) string {
	var out bytes.Buffer
	b, _ := json.Marshal(in)
	_ = json.Indent(&out, b, "", "    ")
	return out.String()
}

func IntSliceToStringSlice(a []int) []string {
	var ss []string
	for _, i := range a {
		ss = append(ss, strconv.Itoa(i))
	}
	return ss
}

func CovertInt64ToInt(numInt64 int64) (int, error) {
	numInt := int(numInt64)
	if int64(numInt) != numInt64 {
		return 0, fmt.Errorf("convert numInt64: %d to numInt: %d failed", numInt64, numInt)
	}
	return numInt, nil
}

func CovertUInt64ToInt(numUInt64 uint64) (int, error) {
	numInt := int(numUInt64)
	if numInt < 0 || uint64(numInt) != numUInt64 {
		return 0, fmt.Errorf("convert numUInt64: %d to numInt: %d failed", numUInt64, numInt)
	}
	return numInt, nil
}

type Pair struct {
	Key   string
	Value int
}

func TraverseMapByValueDescending(m map[string]int) []Pair {
	// Extract all key-value pairs from the map and store them in a slice.
	pairs := make([]Pair, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, Pair{k, v})
	}

	// Sort the slice using a custom comparison function that sorts the pairs by their value in descending order.
	sort.SliceStable(pairs, func(i, j int) bool {
		return pairs[i].Value > pairs[j].Value
	})

	return pairs
}

// IntFractionMultiply is a wrapper for this kind of operation:
// ceil(origin * numerator / denominator)
func IntFractionMultiply(origin int, numerator int, denominator int) int {
	return int(math.Ceil(float64(origin) * float64(numerator) / float64(denominator)))
}
