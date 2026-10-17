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

package cpusettopology

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type containerLifecycleState uint8

const (
	containerLifecycleResolved containerLifecycleState = iota + 1
	containerLifecyclePending
	containerLifecycleRetired
)

type containerLifecycleProof struct {
	PodUID        string
	ContainerName string
	ContainerID   string
	RelativePath  string
	DesiredCPUSet machine.CPUSet
	State         containerLifecycleState
}

func (p containerLifecycleProof) clone() containerLifecycleProof {
	p.DesiredCPUSet = p.DesiredCPUSet.Clone()
	return p
}

type containerLifecycleProofSet struct {
	SourceSnapshotDigest string
	OrderedProofs        []containerLifecycleProof
}

type validatedContainerLifecycleProofSet struct {
	SourceSnapshotDigest string
	ValidationDigest     string
	OrderedProofs        []containerLifecycleProof
}

type containerLifecycleProofStaleError struct {
	PodUID        string
	ContainerName string
	FrozenID      string
	CurrentID     string
	Reason        string
}

func (e *containerLifecycleProofStaleError) Error() string {
	return fmt.Sprintf(
		"%v: lifecycle proof stale pod=%q container=%q frozen_id=%q current_id=%q reason=%s",
		topology.ErrCoordinatorPlanStale,
		e.PodUID, e.ContainerName, e.FrozenID, e.CurrentID, e.Reason,
	)
}

func (e *containerLifecycleProofStaleError) Unwrap() error {
	return topology.ErrCoordinatorPlanStale
}

type containerLifecycleProofKey struct {
	podUID        string
	containerName string
}

func validateContainerLifecycleProofs(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	frozen containerLifecycleProofSet,
) (validatedContainerLifecycleProofSet, error) {
	if metaServer == nil {
		return validatedContainerLifecycleProofSet{}, fmt.Errorf("meta server is required to validate lifecycle proofs")
	}
	strictCtx := context.WithValue(ctx, metapod.BypassCacheKey, metapod.BypassCacheTrue)
	strictCtx = context.WithValue(strictCtx, metapod.StrictBypassCacheKey, metapod.BypassCacheTrue)
	pods, err := metaServer.GetPodList(strictCtx, nil)
	if err != nil {
		return validatedContainerLifecycleProofSet{}, err
	}

	currentByUID := make(map[string]*v1.Pod, len(pods))
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		podUID := string(pod.UID)
		if _, duplicate := currentByUID[podUID]; duplicate {
			return validatedContainerLifecycleProofSet{}, fmt.Errorf(
				"strict lifecycle snapshot contains duplicate pod UID %q", podUID)
		}
		currentByUID[podUID] = pod
	}
	if len(frozen.OrderedProofs) == 0 {
		canonicalDigest, err := lifecycleSourceSnapshotDigest(
			map[string]struct{}{}, currentByUID)
		if err != nil {
			return validatedContainerLifecycleProofSet{}, err
		}
		return validatedContainerLifecycleProofSet{
			SourceSnapshotDigest: canonicalDigest,
			ValidationDigest:     canonicalDigest,
			OrderedProofs:        []containerLifecycleProof{},
		}, nil
	}

	involvedPods := make(map[string]struct{}, len(frozen.OrderedProofs))
	validated := validatedContainerLifecycleProofSet{
		SourceSnapshotDigest: frozen.SourceSnapshotDigest,
		OrderedProofs:        make([]containerLifecycleProof, len(frozen.OrderedProofs)),
	}
	for i, proof := range frozen.OrderedProofs {
		pod := currentByUID[proof.PodUID]
		ownerExists := pod != nil && podSpecHasContainerName(pod, proof.ContainerName)
		currentID, hasCurrentID := podStatusContainerID(pod, proof.ContainerName)
		switch proof.State {
		case containerLifecycleResolved:
			if !ownerExists {
				return validatedContainerLifecycleProofSet{}, newContainerLifecycleProofStaleError(
					proof, currentID, "resolved owner disappeared")
			}
			if !hasCurrentID || currentID != proof.ContainerID {
				return validatedContainerLifecycleProofSet{}, newContainerLifecycleProofStaleError(
					proof, currentID, "resolved container ID changed")
			}
		case containerLifecyclePending:
			if !ownerExists {
				return validatedContainerLifecycleProofSet{}, newContainerLifecycleProofStaleError(
					proof, currentID, "pending owner disappeared")
			}
			if currentID != proof.ContainerID {
				return validatedContainerLifecycleProofSet{}, newContainerLifecycleProofStaleError(
					proof, currentID, "pending container ID changed")
			}
		case containerLifecycleRetired:
			if ownerExists {
				return validatedContainerLifecycleProofSet{}, newContainerLifecycleProofStaleError(
					proof, currentID, "retired owner reappeared")
			}
		default:
			return validatedContainerLifecycleProofSet{}, fmt.Errorf(
				"invalid lifecycle proof state %d for pod=%q container=%q",
				proof.State, proof.PodUID, proof.ContainerName)
		}
		involvedPods[proof.PodUID] = struct{}{}
		validated.OrderedProofs[i] = proof.clone()
	}

	validationDigest, err := lifecycleSourceSnapshotDigest(involvedPods, currentByUID)
	if err != nil {
		return validatedContainerLifecycleProofSet{}, err
	}
	validated.ValidationDigest = validationDigest
	if validationDigest != frozen.SourceSnapshotDigest {
		return validatedContainerLifecycleProofSet{}, &containerLifecycleProofStaleError{
			Reason: "involved pod owner or identity snapshot changed",
		}
	}
	return validated, nil
}

func newContainerLifecycleProofStaleError(
	proof containerLifecycleProof,
	currentID, reason string,
) *containerLifecycleProofStaleError {
	return &containerLifecycleProofStaleError{
		PodUID:        proof.PodUID,
		ContainerName: proof.ContainerName,
		FrozenID:      proof.ContainerID,
		CurrentID:     currentID,
		Reason:        reason,
	}
}

func containerCPUSetByPodFromFinalSnapshot(
	snapshot *topology.CompleteSnapshot,
	proofs validatedContainerLifecycleProofSet,
	deferredCleanupRels map[string]struct{},
) (map[string]map[string]machine.CPUSet, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("final topology snapshot is required")
	}
	// Deferred cleanup is a convergence concern. Lifecycle finalization keeps
	// pending proofs omitted regardless of whether their leaf has appeared.
	_ = deferredCleanupRels
	out := make(map[string]map[string]machine.CPUSet)
	for _, proof := range proofs.OrderedProofs {
		switch proof.State {
		case containerLifecyclePending, containerLifecycleRetired:
			continue
		case containerLifecycleResolved:
			observed, ok := snapshot.TargetProofCPUs(proof.RelativePath, proof.DesiredCPUSet)
			if !ok {
				return nil, newContainerLifecycleProofStaleError(
					proof, proof.ContainerID, "resolved final leaf is missing")
			}
			if out[proof.PodUID] == nil {
				out[proof.PodUID] = make(map[string]machine.CPUSet)
			}
			out[proof.PodUID][proof.ContainerName] = observed.Clone()
		default:
			return nil, fmt.Errorf(
				"invalid validated lifecycle proof state %d for pod=%q container=%q",
				proof.State, proof.PodUID, proof.ContainerName)
		}
	}
	return out, nil
}

func freezeContainerLifecycleProofs(
	proofs []containerLifecycleProof,
	freshPods map[string]*v1.Pod,
) (containerLifecycleProofSet, error) {
	frozen := containerLifecycleProofSet{
		OrderedProofs: make([]containerLifecycleProof, len(proofs)),
	}
	involvedPods := make(map[string]struct{}, len(proofs))
	logicalKeys := make(map[containerLifecycleProofKey]struct{}, len(proofs))
	containerIDOwners := make(map[string]containerLifecycleProofKey, len(proofs))
	relativePathOwners := make(map[string]containerLifecycleProofKey, len(proofs))
	for i := range proofs {
		proof := proofs[i].clone()
		if proof.PodUID == "" || proof.ContainerName == "" {
			return containerLifecycleProofSet{}, fmt.Errorf(
				"invalid lifecycle proof identity pod=%q container=%q",
				proof.PodUID, proof.ContainerName)
		}
		if proof.State < containerLifecycleResolved || proof.State > containerLifecycleRetired {
			return containerLifecycleProofSet{}, fmt.Errorf(
				"invalid lifecycle proof state %d for pod=%q container=%q",
				proof.State, proof.PodUID, proof.ContainerName)
		}
		if proof.DesiredCPUSet.IsEmpty() {
			return containerLifecycleProofSet{}, fmt.Errorf(
				"empty lifecycle proof cpuset for pod=%q container=%q",
				proof.PodUID, proof.ContainerName)
		}
		key := lifecycleProofLogicalKey(proof.PodUID, proof.ContainerName)
		if _, exists := logicalKeys[key]; exists {
			return containerLifecycleProofSet{}, fmt.Errorf(
				"duplicate lifecycle proof for pod=%q container=%q",
				proof.PodUID, proof.ContainerName)
		}
		logicalKeys[key] = struct{}{}
		if err := validateLifecycleProofState(proof, freshPods[proof.PodUID]); err != nil {
			return containerLifecycleProofSet{}, err
		}
		if proof.ContainerID != "" &&
			(proof.State != containerLifecycleRetired || proof.RelativePath != "") {
			if owner, exists := containerIDOwners[proof.ContainerID]; exists && owner != key {
				return containerLifecycleProofSet{}, fmt.Errorf(
					"container ID %q is owned by both pod=%q container=%q and pod=%q container=%q",
					proof.ContainerID, owner.podUID, owner.containerName, key.podUID, key.containerName)
			}
			containerIDOwners[proof.ContainerID] = key
		}
		if proof.State == containerLifecycleResolved {
			if owner, exists := relativePathOwners[proof.RelativePath]; exists && owner != key {
				return containerLifecycleProofSet{}, fmt.Errorf(
					"relative path %q is owned by both pod=%q container=%q and pod=%q container=%q",
					proof.RelativePath, owner.podUID, owner.containerName, key.podUID, key.containerName)
			}
			relativePathOwners[proof.RelativePath] = key
		}
		involvedPods[proof.PodUID] = struct{}{}
		frozen.OrderedProofs[i] = proof
	}
	sort.Slice(frozen.OrderedProofs, func(i, j int) bool {
		left, right := frozen.OrderedProofs[i], frozen.OrderedProofs[j]
		if left.PodUID != right.PodUID {
			return left.PodUID < right.PodUID
		}
		if left.ContainerName != right.ContainerName {
			return left.ContainerName < right.ContainerName
		}
		if left.ContainerID != right.ContainerID {
			return left.ContainerID < right.ContainerID
		}
		if left.RelativePath != right.RelativePath {
			return left.RelativePath < right.RelativePath
		}
		if left.State != right.State {
			return left.State < right.State
		}
		return left.DesiredCPUSet.String() < right.DesiredCPUSet.String()
	})

	digest, err := lifecycleSourceSnapshotDigest(involvedPods, freshPods)
	if err != nil {
		return containerLifecycleProofSet{}, err
	}
	frozen.SourceSnapshotDigest = digest
	return frozen, nil
}

func lifecycleProofLogicalKey(podUID, containerName string) containerLifecycleProofKey {
	return containerLifecycleProofKey{podUID: podUID, containerName: containerName}
}

func validateLifecycleProofState(proof containerLifecycleProof, freshPod *v1.Pod) error {
	freshID, ok := podStatusContainerID(freshPod, proof.ContainerName)
	ownerExists := freshPod != nil && podSpecHasContainerName(freshPod, proof.ContainerName)
	switch proof.State {
	case containerLifecycleResolved:
		if !ownerExists || !ok || proof.ContainerID == "" || proof.ContainerID != freshID {
			return fmt.Errorf(
				"resolved lifecycle proof identity mismatch for pod=%q container=%q: proof=%q fresh=%q",
				proof.PodUID, proof.ContainerName, proof.ContainerID, freshID)
		}
		if proof.RelativePath == "" {
			return fmt.Errorf(
				"resolved lifecycle proof has empty relative path for pod=%q container=%q id=%q",
				proof.PodUID, proof.ContainerName, proof.ContainerID)
		}
	case containerLifecyclePending:
		if !ownerExists || proof.ContainerID != freshID {
			return fmt.Errorf(
				"pending lifecycle proof identity mismatch for pod=%q container=%q: proof=%q fresh=%q",
				proof.PodUID, proof.ContainerName, proof.ContainerID, freshID)
		}
		if proof.RelativePath != "" {
			return fmt.Errorf(
				"pending lifecycle proof has relative path %q for pod=%q container=%q",
				proof.RelativePath, proof.PodUID, proof.ContainerName)
		}
	case containerLifecycleRetired:
		if ownerExists {
			return fmt.Errorf(
				"retired lifecycle proof still has fresh owner pod=%q container=%q",
				proof.PodUID, proof.ContainerName)
		}
	}
	return nil
}

func validateLifecycleProofCoverage(
	desiredByPod map[string]map[string]machine.CPUSet,
	proofs []containerLifecycleProof,
) error {
	expected := make(map[containerLifecycleProofKey]machine.CPUSet)
	for podUID, desiredByContainer := range desiredByPod {
		for containerName, cpus := range desiredByContainer {
			if cpus.IsEmpty() {
				continue
			}
			expected[lifecycleProofLogicalKey(podUID, containerName)] = cpus
		}
	}

	seen := make(map[containerLifecycleProofKey]struct{}, len(proofs))
	var errs []error
	for _, proof := range proofs {
		key := lifecycleProofLogicalKey(proof.PodUID, proof.ContainerName)
		if _, duplicate := seen[key]; duplicate {
			errs = append(errs, fmt.Errorf(
				"duplicate lifecycle proof for pod=%q container=%q",
				proof.PodUID, proof.ContainerName))
			continue
		}
		seen[key] = struct{}{}
		desired, exists := expected[key]
		if !exists {
			errs = append(errs, fmt.Errorf(
				"extra lifecycle proof for pod=%q container=%q",
				proof.PodUID, proof.ContainerName))
			continue
		}
		if !proof.DesiredCPUSet.Equals(desired) {
			errs = append(errs, fmt.Errorf(
				"lifecycle proof cpuset mismatch for pod=%q container=%q: proof=%s desired=%s",
				proof.PodUID, proof.ContainerName,
				proof.DesiredCPUSet.String(), desired.String()))
		}
	}
	for key := range expected {
		if _, exists := seen[key]; exists {
			continue
		}
		errs = append(errs, fmt.Errorf(
			"missing lifecycle proof for pod=%q container=%q",
			key.podUID, key.containerName))
	}
	return apierrors.NewAggregate(errs)
}

func lifecycleSourceSnapshotDigest(
	involvedPods map[string]struct{},
	freshPods map[string]*v1.Pod,
) (string, error) {
	podUIDs := make([]string, 0, len(involvedPods))
	for podUID := range involvedPods {
		podUIDs = append(podUIDs, podUID)
	}
	sort.Strings(podUIDs)

	hash := sha256.New()
	writeField := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	for _, podUID := range podUIDs {
		writeField("pod")
		writeField(podUID)
		pod := freshPods[podUID]
		if pod == nil {
			writeField("absent")
			continue
		}
		if string(pod.UID) != podUID {
			return "", fmt.Errorf("fresh pod map key %q does not match UID %q", podUID, pod.UID)
		}
		writeField("present")

		owners := canonicalPodSpecOwnerNames(pod)
		for _, owner := range owners {
			writeField("owner")
			writeField(owner)
			containerID, _ := podStatusContainerID(pod, owner)
			writeField(containerID)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalPodSpecOwnerNames(pod *v1.Pod) []string {
	owners := make(map[string]struct{})
	for _, container := range pod.Spec.Containers {
		owners[container.Name] = struct{}{}
	}
	for _, container := range pod.Spec.InitContainers {
		owners[container.Name] = struct{}{}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		owners[container.Name] = struct{}{}
	}
	out := make([]string, 0, len(owners))
	for owner := range owners {
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}
