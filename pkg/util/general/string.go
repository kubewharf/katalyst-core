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
	"encoding/json" // nolint: byted_substitute_packages
	"fmt"
	"strconv"
	"strings"
)

func StructToString(val interface{}) string {
	if val == nil {
		return ""
	}
	data, err := json.Marshal(val)
	if err != nil {
		return fmt.Sprintf("%+v", val)
	}
	return BytesToString(data)
}

func IntSliceToString(arr []int) string {
	strArr := make([]string, len(arr))
	for i, v := range arr {
		strArr[i] = strconv.Itoa(v)
	}
	return strings.Join(strArr, ",")
}

func BytesToString(b []byte) string {
	return string(b)
}

// TruncateString truncates the string to the first n characters
func TruncateString(s string, n int) string {
	runeSlice := []rune(s)
	if len(runeSlice) > n {
		return string(runeSlice[:n])
	}
	return s
}
