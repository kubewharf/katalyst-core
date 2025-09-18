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

package file

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"

	"github.com/pkg/errors"
)

// JSONFilesEqual unmarshals the contents of JSON files into structs and checks if they are identical
func JSONFilesEqual(path1, path2 string) (bool, error) {
	decode := func(path string) (interface{}, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("failed to open file %s: %w", path, err)
		}
		defer f.Close()
		var obj interface{}
		if err := json.NewDecoder(f).Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				return obj, nil
			}
			return nil, fmt.Errorf("failed to decode file %s: %w", path, err)
		}
		return obj, nil
	}

	obj1, err := decode(path1)
	if err != nil {
		return false, err
	}
	obj2, err := decode(path2)
	if err != nil {
		return false, err
	}

	return reflect.DeepEqual(obj1, obj2), nil
}
