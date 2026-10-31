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

// Package utils hosts shared, non-domain building blocks for the bulkhead
// subsystem. This file defines the generic CheckpointStore abstraction.
package utils

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckpointCodec owns the (de)serialization and integrity rules of one kind of
// checkpoint payload. It is deliberately separated from CheckpointStore: the
// three existing CPU dynamic-policy checkpoint mechanisms each have their own
// version gating, checksum digest, and domain validation (e.g. CPU-topology
// bounds, whole-core alignment, duplicate detection), and those rules must stay
// with the payload rather than be invented here.
type CheckpointCodec interface {
	// Marshal encodes the payload into durable bytes.
	Marshal() ([]byte, error)

	// Unmarshal decodes durable bytes into the receiver. Implementations MUST
	// reject unknown fields, check the embedded version, and recompute/verify the
	// embedded checksum before returning success. A corrupt payload returns an
	// error; the caller decides whether to fail closed or reset to empty.
	Unmarshal(data []byte) error

	// New returns a fresh, empty payload of the same concrete type, used as the
	// destination for Recover.
	New() CheckpointCodec
}

// CheckpointStore is the crash-safe, atomic file protocol shared by the three
// hand-written checkpoint mechanisms in the CPU dynamic policy:
//
//  1. cpuset-adjustment / advisor post-commit WAL (cpuset_adjustment_handler.go)
//  2. steady fake-NUMA migration target checkpoint
//     (steady_fake_numa_migration_checkpoint.go)
//  3. the advisor post-commit target persisted under the same checkpoint dir.
//
// All three independently converged on the same write protocol:
//
//	mkdir -p dir (0750) -> write temp file in dir (0600) -> fsync temp ->
//	close -> atomic rename over target -> fsync parent directory
//
// and the same recover protocol:
//
//	read target (missing => empty state, nil error) -> decode with unknown-field
//	rejection -> version gate -> checksum verify -> domain validation -> restore
//
// CheckpointStore standardizes only that file protocol. It does NOT interpret
// payload semantics: version gates, checksums, and topology validation remain
// inside the CheckpointCodec.
//
// NOTE (migration status): two of the three hand-written mechanisms have already
// moved onto FileCheckpointStore and keep using it as their crash-safe protocol:
//   - the cpuset-adjustment / advisor post-commit WAL and the advisor post-commit
//     target share one store (cpuset_adjustment_handler.go, see
//     advisorPostCommitCheckpointStore);
//   - the steady fake-NUMA migration target checkpoint
//     (steady_fake_numa_migration_checkpoint.go).
//
// The remaining legacy mechanism is the QRM allocation-state checkpoint
// (state/state_checkpoint.go, written through checkpointmanager). It still uses
// its own on-disk protocol and remains the source of truth; migrating it is a
// data-safety critical change and needs a dedicated PR that keeps the existing
// recovery tests green and adds crash/recovery coverage (kill mid-rename, corrupt
// checksum, version skew, partial write).
type CheckpointStore interface {
	// Store atomically persists the codec-encoded payload for name. On success,
	// a crash leaves either the previous or the new checkpoint, never a torn
	// file. On failure, the previous checkpoint is left untouched.
	Store(name string, codec CheckpointCodec) error

	// Recover reads and validates the checkpoint for name. factory is a blank
	// codec instance used only to mint a fresh destination via its New() method;
	// it carries no data. A missing checkpoint returns (nil, nil) so the caller
	// treats it as absent; a corrupt, wrong-version, or checksum-mismatched
	// checkpoint returns an error.
	Recover(name string, factory CheckpointCodec) (CheckpointCodec, error)

	// Remove deletes the checkpoint for name; removing an absent checkpoint is a
	// no-op. It fsyncs the parent directory so the deletion is durable.
	Remove(name string) error

	// Promote atomically renames the staging checkpoint to the active checkpoint
	// name within the same store directory, then fsyncs the parent directory. It
	// is the crash-safe half of the dual-file write-ahead log used by the
	// advisor post-commit WAL: an intent is first written durably with
	// Store(stagingName), the canonical state is advanced, and only then is the
	// staging record promoted to activeName. Because rename(2) on a single file
	// system is atomic, a crash either leaves the previous active record, the
	// staging record, or — once the rename and dir-fsync land — the new active
	// record; it never leaves a torn or partially-written active file. Promote
	// propagates the rename error when stagingName does not exist, so callers
	// distinguish "nothing to promote" from a real I/O failure.
	Promote(stagingName, activeName string) error
}

// FileCheckpointStore is the reference on-disk CheckpointStore. It writes every
// checkpoint under Dir using the temp-file -> fsync -> rename -> dir-fsync
// protocol described on CheckpointStore. It is safe for concurrent use only if
// the caller serializes Store/Remove for the same name.
type FileCheckpointStore struct {
	// Dir is the directory that holds checkpoints. Created on first Store.
	Dir string
}

// NewFileCheckpointStore returns a FileCheckpointStore rooted at dir.
func NewFileCheckpointStore(dir string) *FileCheckpointStore {
	return &FileCheckpointStore{Dir: dir}
}

// compile-time assertion that *FileCheckpointStore satisfies CheckpointStore.
var _ CheckpointStore = (*FileCheckpointStore)(nil)

// checkpointPath joins the store directory with a checkpoint name.
func (s *FileCheckpointStore) checkpointPath(name string) string {
	return filepath.Join(s.Dir, name)
}

// Store implements CheckpointStore.
func (s *FileCheckpointStore) Store(name string, codec CheckpointCodec) error {
	if s.Dir == "" || name == "" || codec == nil {
		return nil
	}
	data, err := codec.Marshal()
	if err != nil {
		return fmt.Errorf("marshal checkpoint %q: %w", name, err)
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return fmt.Errorf("create checkpoint directory %q: %w", s.Dir, err)
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+"-*")
	if err != nil {
		return fmt.Errorf("create temporary checkpoint %q: %w", name, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temporary checkpoint %q: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary checkpoint %q: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary checkpoint %q: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary checkpoint %q: %w", name, err)
	}
	if err := os.Rename(tmpPath, s.checkpointPath(name)); err != nil {
		return fmt.Errorf("publish checkpoint %q: %w", name, err)
	}
	return syncDirectory(s.checkpointPath(name))
}

// Recover implements CheckpointStore.
func (s *FileCheckpointStore) Recover(name string, factory CheckpointCodec) (CheckpointCodec, error) {
	if s.Dir == "" || name == "" || factory == nil {
		return nil, nil
	}
	path := s.checkpointPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read checkpoint %q: %w", name, err)
	}
	codec := factory.New()
	if err := codec.Unmarshal(data); err != nil {
		return nil, fmt.Errorf("decode checkpoint %q: %w", name, err)
	}
	return codec, nil
}

// Remove implements CheckpointStore.
func (s *FileCheckpointStore) Remove(name string) error {
	path := s.checkpointPath(name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove checkpoint %q: %w", name, err)
	}
	return syncDirectory(path)
}

// Promote implements CheckpointStore.
func (s *FileCheckpointStore) Promote(stagingName, activeName string) error {
	if s.Dir == "" || stagingName == "" || activeName == "" {
		return nil
	}
	stagingPath := s.checkpointPath(stagingName)
	activePath := s.checkpointPath(activeName)
	if err := os.Rename(stagingPath, activePath); err != nil {
		return fmt.Errorf("promote checkpoint %q to %q: %w", stagingName, activeName, err)
	}
	return syncDirectory(activePath)
}

// syncDirectory fsyncs the parent directory of path so a preceding
// create/rename/unlink is durable.
func syncDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open checkpoint directory for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync checkpoint directory: %w", err)
	}
	return nil
}
