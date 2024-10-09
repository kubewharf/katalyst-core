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

func DeepCopyMap(origin map[string]string) map[string]string {
	if origin == nil {
		return nil
	}

	res := make(map[string]string, len(origin))
	for key, val := range origin {
		res[key] = val
	}
	return res
}

func DeepCopyIntMap(origin map[string]int) map[string]int {
	if origin == nil {
		return nil
	}

	res := make(map[string]int, len(origin))
	for key, val := range origin {
		res[key] = val
	}
	return res
}

func DeepCopyIntToIntMap(origin map[int]int) map[int]int {
	if origin == nil {
		return nil
	}

	res := make(map[int]int, len(origin))
	for key, val := range origin {
		res[key] = val
	}
	return res
}

func DeepCopyIntToFloat64Map(origin map[int]float64) map[int]float64 {
	if origin == nil {
		return nil
	}

	res := make(map[int]float64, len(origin))
	for key, val := range origin {
		res[key] = val
	}
	return res
}
