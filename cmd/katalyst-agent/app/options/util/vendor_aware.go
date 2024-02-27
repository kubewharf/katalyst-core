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

package util

import (
	"fmt"

	cpu "github.com/klauspost/cpuid/v2"
)

const (
	DefaultConfigKey = "default"

	CPUVendorAMD   = "AMD"
	CPUVendorIntel = "Intel"
)

var supportCPUVendors = map[cpu.Vendor]string{
	cpu.AMD:   CPUVendorAMD,
	cpu.Intel: CPUVendorIntel,
}

func GetVendorAwareStringConfiguration(vendorConfMap map[string]string) (string, error) {
	var result string
	if defaultValue, ok := vendorConfMap[DefaultConfigKey]; ok {
		result = defaultValue
	} else {
		return "", fmt.Errorf("vendor aware configuration must have a default value")
	}

	vendorID := cpu.CPU.VendorID
	if vendorKey, ok := supportCPUVendors[vendorID]; ok {
		if value, exist := vendorConfMap[vendorKey]; exist {
			result = value
		}
	}
	return result, nil
}

func GetVendorAwareInt64Configuration(vendorConfMap map[string]int64) (int64, error) {
	var result int64
	if defaultValue, ok := vendorConfMap[DefaultConfigKey]; ok {
		result = defaultValue
	} else {
		return 0, fmt.Errorf("vendor aware configuration must have a default value")
	}

	vendorID := cpu.CPU.VendorID
	if vendorKey, ok := supportCPUVendors[vendorID]; ok {
		if value, exist := vendorConfMap[vendorKey]; exist {
			result = value
		}
	}
	return result, nil
}
