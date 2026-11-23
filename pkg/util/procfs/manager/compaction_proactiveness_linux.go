//go:build linux
// +build linux

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

package manager

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// ReadVMCompactionProactivenessAtPath validates the kernel range before narrowing
// the value. The path is deployment-owned, never an untrusted request parameter.
func ReadVMCompactionProactivenessAtPath(path string) (int32, error) {
	value, err := general.ReadInt64FromFile(path)
	if err != nil {
		return 0, err
	}
	if value < 0 || value > 100 {
		return 0, fmt.Errorf("proactiveness outside kernel range: %d", value)
	}
	return int32(value), nil
}

// WriteVMCompactionProactivenessAtPath writes without creating or truncating the
// sysctl. A successful write is not readback confirmation or lease renewal.
func WriteVMCompactionProactivenessAtPath(path string, value int32) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("proactiveness outside kernel range: %d", value)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	data := []byte(strconv.FormatInt(int64(value), 10) + "\n")
	n, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return closeErr
}
