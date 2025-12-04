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

package flag

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	corev1helper "k8s.io/kubernetes/pkg/apis/core/v1/helper"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var _ pflag.Value = &ReservedMemoryVar{}

// ReservedMemoryVar is used for validating a command line option that represents a reserved memory. It implements the pflag.Value interface
type ReservedMemoryVar struct {
	Value       *[]native.MemoryReservation
	initialized bool // set to true after the first Set call
}

// Set sets the flag value
func (v *ReservedMemoryVar) Set(s string) error {
	if v.Value == nil {
		return fmt.Errorf("no target (nil pointer to *[]MemoryReservation)")
	}

	if s == "" {
		v.Value = nil
		return nil
	}

	if !v.initialized || *v.Value == nil {
		*v.Value = make([]native.MemoryReservation, 0)
		v.initialized = true
	}

	numaNodeReservations := strings.Split(s, "/")
	for _, reservation := range numaNodeReservations {
		numaNodeReservation := strings.Split(reservation, ":")
		if len(numaNodeReservation) != 2 {
			return fmt.Errorf("the reserved memory has incorrect format, expected numaNodeID:type=quantity[,type=quantity...], got %s", reservation)
		}
		memoryTypeReservations := strings.Split(numaNodeReservation[1], ",")
		if len(memoryTypeReservations) < 1 {
			return fmt.Errorf("the reserved memory has incorrect format, expected numaNodeID:type=quantity[,type=quantity...], got %s", reservation)
		}
		numaNodeID, err := strconv.Atoi(numaNodeReservation[0])
		if err != nil {
			return fmt.Errorf("failed to convert the NUMA node ID, expected integer, got %s", numaNodeReservation[0])
		}

		memoryReservation := native.MemoryReservation{
			NumaNode: int32(numaNodeID),
			Limits:   map[v1.ResourceName]resource.Quantity{},
		}

		for _, memoryTypeReservation := range memoryTypeReservations {
			limit := strings.Split(memoryTypeReservation, "=")
			if len(limit) != 2 {
				return fmt.Errorf("the reserved limit has incorrect value, expected type=quantity, got %s", memoryTypeReservation)
			}

			resourceName := v1.ResourceName(limit[0])
			if resourceName != v1.ResourceMemory && !corev1helper.IsHugePageResourceName(resourceName) {
				return fmt.Errorf("memory type conversion error, unknown type: %q", resourceName)
			}

			q, err := resource.ParseQuantity(limit[1])
			if err != nil {
				return fmt.Errorf("failed to parse the quantity: %s", limit[1])
			}

			memoryReservation.Limits[v1.ResourceName(limit[0])] = q
		}
		*v.Value = append(*v.Value, memoryReservation)
	}
	return nil
}

// String returns the flag value
func (v *ReservedMemoryVar) String() string {
	if v == nil || v.Value == nil {
		return ""
	}

	var slices []string
	for _, reservedMemory := range *v.Value {
		var limits []string
		for resourceName, q := range reservedMemory.Limits {
			limits = append(limits, fmt.Sprintf("%s=%s", resourceName, q.String()))
		}

		sort.Strings(limits)
		slices = append(slices, fmt.Sprintf("%d:%s", reservedMemory.NumaNode, strings.Join(limits, ",")))
	}

	sort.Strings(slices)
	return strings.Join(slices, ",")
}

// Type gets the flag type
func (v *ReservedMemoryVar) Type() string {
	return "reserved-memory"
}
