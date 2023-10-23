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

	res := make(map[string]string)
	for key, val := range origin {
		res[key] = val
	}
	return res
}

func DeepCopyFload64Map(origin map[string]float64) map[string]float64 {
	if origin == nil {
		return nil
	}

	res := make(map[string]float64)
	for key, val := range origin {
		res[key] = val
	}
	return res
}
