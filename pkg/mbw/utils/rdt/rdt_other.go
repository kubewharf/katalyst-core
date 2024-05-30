//go:build !(amd64 && !gccgo && !noasm && !appengine)
// +build !amd64 gccgo noasm appengine

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

package rdt

const (
	PQR_ASSOC int64 = 0xC8F

	PQOS_MON_EVENT_TMEM_BW PQOS_EVENT_TYPE = 2
	PQOS_MON_EVENT_LMEM_BW PQOS_EVENT_TYPE = 3
)

type PQOS_EVENT_TYPE uint64

func GetRDTScalar() uint32 {
	panic("not available")
}

func GetRDTValue(core uint32, event PQOS_EVENT_TYPE) (uint64, error) {
	panic("not available")
}
