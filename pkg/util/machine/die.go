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

// DieTopology keeps the relationship of dies(CCDs), numa, package, and socket
type DieTopology struct {
	NumPackages      int           // physical NUMA amount on this server
	PackageMap       map[int][]int // mapping from Package to Numa nodes
	PackagePerSocket int           // pakcage amount on a socket
	RDTScalar        uint32        // get the per-core memory bandwidth by multiplying rdt events with this coefficient
	DieSize          int           // how many cores on a die (i.e. a CCD)
	NumCCDs          int           // the number of CCDs on this machine
	CCDMap           map[int][]int // mapping from CCD to CPU cores on AMD
	NumaMap          map[int][]int // mapping from Numa to CCDs on AMD
	FakeNUMAEnabled  bool          // if the fake NUMA is configured on this server
}
