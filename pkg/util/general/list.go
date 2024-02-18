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

import "reflect"

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
