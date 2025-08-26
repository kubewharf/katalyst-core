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
	"fmt"
	"reflect"
	"sort"
)

// Int64Slice attaches the methods of Interface to []int64, sorting in increasing order.
type Int64Slice []int64

func (x Int64Slice) Len() int           { return len(x) }
func (x Int64Slice) Less(i, j int) bool { return x[i] < x[j] }
func (x Int64Slice) Swap(i, j int)      { x[i], x[j] = x[j], x[i] }

// SortInt64Slice sorts a slice of int64 in increasing order.
func SortInt64Slice(x []int64) {
	sort.Sort(Int64Slice(x))
}

// SliceContains returns true if an element is present in a slice.
func SliceContains(list interface{}, elem interface{}) bool {
	if list == nil || elem == nil {
		return false
	}
	listV := reflect.ValueOf(list)

	if listV.Kind() == reflect.Slice {
		for i := 0; i < listV.Len(); i++ {
			item := listV.Index(i).Interface()

			target := reflect.ValueOf(elem).Convert(reflect.TypeOf(item)).Interface()
			if ok := reflect.DeepEqual(item, target); ok {
				return true
			}
		}
	}
	return false
}

func GetSlicesIntersection(a []int64, b []int64) []int64 {
	var c []int64

	for _, i := range a {
		for _, j := range b {
			if i == j {
				c = append(c, i)
				break
			}
		}
	}

	return c
}

func GetSlicesDiff(a []int64, b []int64) []int64 {
	var c []int64

	for _, i := range a {
		found := false
		for _, j := range b {
			if i == j {
				found = true
				break
			}
		}
		if !found {
			c = append(c, i)
		}
	}

	return c
}

func IntSliceHasOverlap(a, b []int) bool {
	hasOverlap := false

	for _, i := range a {
		for _, j := range b {
			if i == j {
				hasOverlap = true
				break
			}
		}
		if hasOverlap {
			break
		}
	}

	return hasOverlap
}

func GetIntersectionOfTwoIntSlices(a, b []int) []int {
	var intersection []int

	for _, i := range a {
		for _, j := range b {
			if i == j {
				intersection = append(intersection, i)
			}
		}
	}

	return intersection
}

func ConvertInt64SliceToIntSlice(a []int64) []int {
	var b []int
	for _, i := range a {
		b = append(b, int(i))
	}
	return b
}

func ConvertIntSliceToBitmapString(nums []int64) (string, error) {
	if len(nums) == 0 {
		return "", nil
	}

	maxVal := int64(-1)
	for _, v := range nums {
		if v < 0 {
			return "", fmt.Errorf("nums contains negtive value")
		}

		if v > maxVal {
			maxVal = v
		}
	}

	length := (maxVal / 32) + 1
	bitmap := make([]uint32, length)

	for _, i := range nums {
		index := i / 32
		shift := i % 32
		subBitmap := bitmap[index]
		subBitmap |= (1 << shift)
		bitmap[index] = subBitmap
	}

	bitmapStr := ""
	for i := len(bitmap) - 1; i >= 0; i-- {
		if bitmapStr == "" {
			bitmapStr = fmt.Sprintf("%08x", bitmap[i])
		} else {
			bitmapStr = fmt.Sprintf("%s,%08x", bitmapStr, bitmap[i])
		}
	}
	return bitmapStr, nil
}
