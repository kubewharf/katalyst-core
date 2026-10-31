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

package dynamicpolicy

import (
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
)

// Strict annotation values.
const (
	cpusetOmissionTrue  = "true"
	cpusetOmissionFalse = "false"
)

// hasBypassableQoS reports whether the allocation's QoS level is eligible to
// skip response cpuset backfill at all. It delegates to qosIsBypassable so the
// QoS predicate lives in exactly one place.
func hasBypassableQoS(info *state.AllocationInfo) bool {
	return info != nil && qosIsBypassable(info.QoSLevel)
}

// qosIsBypassable reports whether the given QoS level string is eligible.
func qosIsBypassable(qosLevel string) bool {
	return qosLevel == apiconsts.PodAnnotationQoSLevelSharedCores ||
		qosLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores ||
		qosLevel == apiconsts.PodAnnotationQoSLevelSystemCores
}

// cpusetOmissionAllowed reports whether the live bypass decision allows omitting
// the CPUSet in the response. It combines the live switch with the recorded
// cpuset-omission-allowed annotation:
//   - dedicated / non-bypassable QoS, or live switch off: always false;
//   - the annotation present: "true" -> true; "false"/any other value -> false
//     (fail-closed);
//   - annotation absent: derive from QoS (true when bypassable).
func cpusetOmissionAllowed(info *state.AllocationInfo, liveEnabled bool) bool {
	if !liveEnabled || info == nil || !hasBypassableQoS(info) {
		return false
	}
	val, present := info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
	if !present {
		return true
	}
	return val == cpusetOmissionTrue
}

// initialCPUSetOmissionAllowed computes the annotation value to write on a first
// Allocate for a brand-new container:
//   - dedicated / non-bypassable QoS, or live off: false;
//   - request carries the key: explicit "true" permits; "false"/any other value
//     fails closed;
//   - request omits the key: QoS-derived default (true when bypassable).
func initialCPUSetOmissionAllowed(qosLevel string, requestAnnotations map[string]string, liveEnabled bool) bool {
	if !qosIsBypassable(qosLevel) || !liveEnabled {
		return false
	}
	if val, present := requestAnnotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]; present {
		return val == cpusetOmissionTrue
	}
	return true
}

// needsCPUSetDemotion reports whether the entry actually needs a one-way true->false
// write: it is bypassable, has a non-empty CPUSet to expose, the live decision does
// NOT currently allow omission, and the recorded annotation is not already "false".
// live=false still requires demoting a persisted "true"/missing/invalid entry so the
// value cannot resurrect when the watcher flips back on; an entry already recording
// "false" never needs another write.
func needsCPUSetDemotion(info *state.AllocationInfo, liveEnabled bool) bool {
	if info == nil || !hasBypassableQoS(info) || info.AllocationResult.IsEmpty() {
		return false
	}
	if cpusetOmissionAllowed(info, liveEnabled) {
		return false
	}
	val, present := info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
	return !present || val != cpusetOmissionFalse
}

// markCPUSetExposed sets the annotation to "false" on the given allocationInfo
// clone. This is a one-way downgrade: it never writes "true".
//
// Returns true if the annotation was changed (and thus needs persistence):
//   - dedicated / non-bypassable QoS: no-op (false);
//   - entries already recording "false": no-op (false);
//   - entries with nil Annotations, missing key, "true", or invalid value
//     (but QoS-bypassable): create map if needed and set to "false" (true).
func markCPUSetExposed(info *state.AllocationInfo) bool {
	if info == nil || !hasBypassableQoS(info) {
		return false
	}
	key := cpuconsts.CPUSetOmissionAllowedAnnotationKey
	val, present := info.Annotations[key]
	if present && val == cpusetOmissionFalse {
		return false // already false
	}
	if info.Annotations == nil {
		info.Annotations = make(map[string]string)
	}
	info.Annotations[key] = cpusetOmissionFalse
	return true
}

// initNewMainContainerCPUSetOmission records the initial cpuset-omission-allowed
// value on a brand-new MAIN container entry. It is the ONLY place the annotation
// is born: existing/resize/reuse entries inherit it via inheritCPUSetOmission, and
// afterwards it can only ever move true -> false (see markCPUSetExposed). It writes
// an explicit "true"/"false" so newly-admitted containers do not rely on the
// missing-key QoS derivation. It is a no-op for a nil info.
func initNewMainContainerCPUSetOmission(info *state.AllocationInfo, reqAnnotations map[string]string, liveEnabled bool) {
	if info == nil {
		return
	}
	if info.Annotations == nil {
		info.Annotations = make(map[string]string)
	}
	if initialCPUSetOmissionAllowed(info.QoSLevel, reqAnnotations, liveEnabled) {
		info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey] = cpusetOmissionTrue
	} else {
		info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey] = cpusetOmissionFalse
	}
}

// inheritCPUSetOmission copies the cpuset-omission-allowed annotation from origin
// to dst. If origin has the key, dst gets the same value. If origin lacks the key
// (or origin is nil), dst's key is deleted. This ensures that on resize/reuse
// paths, request annotations cannot leak or upgrade the persisted value.
func inheritCPUSetOmission(dst, origin *state.AllocationInfo) {
	if dst == nil {
		return
	}
	key := cpuconsts.CPUSetOmissionAllowedAnnotationKey
	if origin == nil || origin.Annotations == nil {
		delete(dst.Annotations, key)
		return
	}
	val, present := origin.Annotations[key]
	if !present {
		delete(dst.Annotations, key)
		return
	}
	if dst.Annotations == nil {
		dst.Annotations = make(map[string]string)
	}
	dst.Annotations[key] = val
}
