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

package domain

import "k8s.io/apimachinery/pkg/util/sets"

// Domain is the unit of memory bandwidth to share and compete with
type Domain struct {
	ID   int
	CCDs sets.Int

	// below are for incoming memory bandwidth
	CapacityInMB    int
	MinMBPerCCD     int
	MaxMBPerCCD     int
	ccdAlienMBLimit int
}

func (d *Domain) GetAlienMBLimit() int {
	return d.ccdAlienMBLimit * d.CCDs.Len()
}

func NewDomain(id int, ccds sets.Int, capacity, ccdMax, ccdMin, ccdAlienMBLimit int) *Domain {
	domain := Domain{
		ID:              id,
		CCDs:            sets.NewInt(ccds.List()...),
		CapacityInMB:    capacity,
		MinMBPerCCD:     ccdMin,
		MaxMBPerCCD:     ccdMax,
		ccdAlienMBLimit: ccdAlienMBLimit,
	}

	return &domain
}
