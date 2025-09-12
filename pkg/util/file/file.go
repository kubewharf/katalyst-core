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
	"bytes"
	"fmt"
	"io"
	"os"
)

const bufSize = 32 * 1024

// FilesEqual compares 2 files by their contents and returns true if they are equal
func FilesEqual(path1, path2 string) (bool, error) {
	f1, err := os.Open(path1)
	if err != nil {
		return false, fmt.Errorf("failed to open file %s: %w", path1, err)
	}
	defer f1.Close()

	f2, err := os.Open(path2)
	if err != nil {
		return false, fmt.Errorf("failed to open file %s: %w", path2, err)
	}
	defer f2.Close()

	// Compare file sizes first
	info1, err := f1.Stat()
	if err != nil {
		return false, fmt.Errorf("failed to get stat for file %s: %w", path1, err)
	}
	info2, err := f2.Stat()
	if err != nil {
		return false, fmt.Errorf("failed to get stat for file %s: %w", path2, err)
	}
	if info1.Size() != info2.Size() {
		return false, nil
	}

	// Read and compare chunks
	b1 := make([]byte, bufSize)
	b2 := make([]byte, bufSize)

	for {
		n1, err1 := f1.Read(b1)
		n2, err2 := f2.Read(b2)

		if n1 != n2 || !bytes.Equal(b1[:n1], b2[:n2]) {
			return false, nil
		}
		if err1 == io.EOF && err2 == io.EOF {
			break
		}
		if err1 != nil && err2 != io.EOF {
			return false, err1
		}
		if err2 != nil && err2 != io.EOF {
			return false, err2
		}
	}
	return true, nil
}
