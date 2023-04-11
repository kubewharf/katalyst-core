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
	"reflect"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
)

// DeepCopiable is an interface that can be implemented by types that need to
// customize their deep copy behavior.
type DeepCopiable interface {
	DeepCopy() interface{}
}

// DeepCopy returns a deep copy of the given object.
func DeepCopy(src interface{}) interface{} {
	if src == nil {
		return nil
	}

	old := reflect.ValueOf(src)
	cpy := reflect.New(old.Type()).Elem()
	copyRecursive(old, cpy)
	return cpy.Interface()
}

func copyRecursive(oldItem, newItem reflect.Value) {
	if oldItem.CanInterface() {
		if copyItem, ok := oldItem.Interface().(DeepCopiable); ok {
			newItem.Set(reflect.ValueOf(copyItem.DeepCopy()))
			return
		}
	}

	switch oldItem.Kind() {
	case reflect.Ptr:
		oldValue := oldItem.Elem()

		if !oldValue.IsValid() {
			return
		}

		newItem.Set(reflect.New(oldValue.Type()))
		copyRecursive(oldValue, newItem.Elem())
	case reflect.Interface:
		if oldItem.IsNil() {
			return
		}
		oldValue := oldItem.Elem()

		copyValue := reflect.New(oldValue.Type()).Elem()
		copyRecursive(oldValue, copyValue)
		newItem.Set(copyValue)
	case reflect.Struct:
		// check for some native struct copy
		if nativeStructCopy(oldItem, newItem) {
			return
		}

		for i := 0; i < oldItem.NumField(); i++ {
			if oldItem.Type().Field(i).PkgPath != "" {
				continue
			}
			copyRecursive(oldItem.Field(i), newItem.Field(i))
		}
	case reflect.Slice:
		if oldItem.IsNil() {
			return
		}

		newItem.Set(reflect.MakeSlice(oldItem.Type(), oldItem.Len(), oldItem.Cap()))
		for i := 0; i < oldItem.Len(); i++ {
			copyRecursive(oldItem.Index(i), newItem.Index(i))
		}
	case reflect.Map:
		if oldItem.IsNil() {
			return
		}

		newItem.Set(reflect.MakeMap(oldItem.Type()))
		for _, key := range oldItem.MapKeys() {
			oldValue := oldItem.MapIndex(key)
			newValue := reflect.New(oldValue.Type()).Elem()
			copyRecursive(oldValue, newValue)
			copyKey := DeepCopy(key.Interface())
			newItem.SetMapIndex(reflect.ValueOf(copyKey), newValue)
		}
	default:
		newItem.Set(oldItem)
	}
}

// nativeStructCopy support copy function for some native struct
func nativeStructCopy(oldItem, newItem reflect.Value) bool {
	if !oldItem.CanInterface() {
		return false
	}

	switch copier := oldItem.Interface().(type) {
	case time.Time:
		newItem.Set(reflect.ValueOf(copier))
		return true
	case v1.ResourceList:
		newItem.Set(reflect.ValueOf(copier.DeepCopy()))
		return true
	case resource.Quantity:
		newItem.Set(reflect.ValueOf(copier.DeepCopy()))
		return true
	case labels.Selector:
		newItem.Set(reflect.ValueOf(copier.DeepCopySelector()))
		return true
	}

	return false
}
