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

package machine

import "github.com/spf13/afero"

// FakeNUMAEnabled is Bytedance specific approach to check whether fake numa applicable to this machine
// Alternative ways exist, e.g. looking at distances between numa nodes; this firmware file content check
// is preferred for its simplicity and reliability (given the vendor)
func FakeNUMAEnabled(fs afero.Fs) (bool, error) {
	slitPath := "/sys/firmware/acpi/tables/SLIT"

	f, err := fs.Open(slitPath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	// typical SLIT file has 108 bytes; allocate buffer 1024 bytes just in case
	buffer := make([]byte, 1024)
	var n int
	if n, err = f.Read(buffer); err != nil {
		return false, err
	}

	for i := 0; i < n-4; i += 2 {
		if buffer[i] == 0x88 && buffer[i+1] == 0x88 && buffer[i+2] == 0x88 && buffer[i+3] == 0x88 {
			return true, nil
		}
	}

	return false, nil
}
