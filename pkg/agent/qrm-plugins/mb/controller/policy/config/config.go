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

package config

const (
	DomainTotalMB   = 120_000             //120 GBps in one mb sharing domain
	ReservedPerNuma = 35_000              // 35 GBps reserved per node for dedicated pod
	ReservedPerCCD  = ReservedPerNuma / 2 // hardcoded divisor 2 may not be applicable all the places

	CCDMBMin = 8_000 // per CCD 8 GB

	// set max of CCD mb even higher, hopefully the initial spike efffect would be obliviated
	// todo: to fix test cases (which expect original 30GB) after the 35 is finalized
	CCDMBMax = 35_000 // per CCD 35 GB (hopefully effective 25GB)
)
