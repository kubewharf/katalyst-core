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

package syntax

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// MergeValueFunc is to merge src value to dst
type MergeValueFunc func(src reflect.Value, dst reflect.Value) error

// SetSliceOrArrayValue append src slice or array to the end of dst
func SetSliceOrArrayValue(src reflect.Value, dst reflect.Value) {
	if src.Type() != dst.Type() ||
		(src.Kind() != reflect.Slice && src.Kind() != reflect.Array) {
		return
	}

	// initialize a new slice
	newSlice := reflect.New(dst.Type()).Elem()

	for i := 0; i < dst.Len(); i++ {
		if dst.Index(i).Kind() == reflect.Ptr && dst.Index(i).IsNil() {
			continue
		}

		newSlice = reflect.Append(newSlice, dst.Index(i))
	}

	for i := 0; i < src.Len(); i++ {
		if src.Index(i).Kind() == reflect.Ptr && src.Index(i).IsNil() {
			continue
		}

		newSlice = reflect.Append(newSlice, src.Index(i))
	}

	// set dst slice with the new slice
	dst.Set(newSlice)
}

// SetMapValue set all key and value from src map to dst map, and the value of
// same key in the dst map will be overwritten
func SetMapValue(src reflect.Value, dst reflect.Value) {
	if src.Type() != dst.Type() ||
		src.Kind() != reflect.Map {
		return
	}

	// initialize a new map
	newMap := reflect.MakeMap(dst.Type())

	// set key and value of dst map to the new map
	iter := dst.MapRange()
	for iter.Next() {
		newMap.SetMapIndex(iter.Key(), iter.Value())
	}

	// set key and value of src map to the new map
	iter = src.MapRange()
	for iter.Next() {
		newMap.SetMapIndex(iter.Key(), iter.Value())
	}

	// set dst with the new map
	dst.Set(newMap)
}

// ParseBytesByType parse []byte to a value with type t
func ParseBytesByType(b []byte, t reflect.Type) (reflect.Value, error) {
	newType := t
	if t.Kind() == reflect.Ptr {
		newType = t.Elem()
	}

	res := reflect.New(newType)
	ptr := res.Interface()

	err := json.Unmarshal(b, &ptr)
	if err != nil {
		return reflect.Value{}, fmt.Errorf("could not unmarshal to: %v", err)
	}

	if t.Kind() != reflect.Ptr {
		if res.IsNil() {
			res = reflect.New(res.Type().Elem())
		}
		res = res.Elem()
	}

	return res, nil
}

// SimpleMergeTwoValues merge two value with same type, which includes 3 cases:
// 1. slice & array (or its pointer): append src slice to dst
// 2. map (or its pointer): src map will overwrite dst
// 3. other (or its pointer): set src by dst
// since SimpleMergeTwoValues is a "simple" one, there may exist some cases
// SimpleMergeTwoValues won't support, and a possible one is a struct with a map
// or slice member, we will not merge its member object.
// for example, consider src and dst with the same type of
/*
type newType struct {
	a []string
}
*/
// and src is newType{"a":["2"]}, dst is newType{"a":["1"]}, the simple merge result
// is newType{"a":["2"]}, and not newType{"a":["1","2"]}
func SimpleMergeTwoValues(src reflect.Value, dst reflect.Value) error {
	if src.Type() != dst.Type() {
		return fmt.Errorf("type of src and dst is inconsistent")
	}

	switch dst.Kind() {
	case reflect.Array, reflect.Slice:
		SetSliceOrArrayValue(src, dst)
	case reflect.Map:
		SetMapValue(src, dst)
	case reflect.Struct:
		var errList []error
		allFieldsCanSet := true
		for i := 0; i < src.NumField(); i++ {
			if !dst.Field(i).CanSet() {
				allFieldsCanSet = false
				break
			}
			if err := SimpleMergeTwoValues(src.Field(i), dst.Field(i)); err != nil {
				errList = append(errList, err)
			}
		}
		if !allFieldsCanSet {
			dst.Set(src)
		}
		if len(errList) > 0 {
			return fmt.Errorf("failed to merge struct: %v", errList)
		}
	case reflect.Ptr:
		// if report value is nil, we no need to update origin value
		if src.IsNil() {
			return nil
		}
		src = src.Elem()

		// initialize the origin value if it is nil
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		dst = dst.Elem()

		// merge their ptr value recursively
		return SimpleMergeTwoValues(src, dst)
	default:
		dst.Set(src)
	}

	return nil
}
