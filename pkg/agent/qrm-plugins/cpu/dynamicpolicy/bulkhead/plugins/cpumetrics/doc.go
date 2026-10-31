/*
Copyright 2026 The Katalyst Authors.

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

// Package cpumetrics samples per-pod/per-NUMA CPU metrics on the hot path.
//
// Performance: allocations are minimized (numaBuckets, label projection) and
// pooled objects reset in a known order. Do not add per-pod map/string work in
// the sample loop without measuring.
package cpumetrics
