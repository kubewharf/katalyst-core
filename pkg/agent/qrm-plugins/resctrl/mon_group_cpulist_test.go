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

package resctrl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resctrlutil "github.com/kubewharf/katalyst-core/pkg/util/resctrl"
	"github.com/stretchr/testify/require"
)

func monGroupFixture(t *testing.T) (*cpuListManager, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), []byte("0000001e"), 0o644))
	return newCPUListManagerWithUpdater(root, nil), root
}

func addMonGroup(t *testing.T, root, name, mask string) {
	t.Helper()
	path := filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name)
	require.NoError(t, os.Mkdir(path, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, cpus), []byte(mask), 0o644))
}

func TestMonGroupManagerWritesAndObservesExistingGroups(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000000")
	addMonGroup(t, root, "podb", "00000000")
	targets := map[string]machine.CPUSet{"poda": machine.NewCPUSet(1, 2), "podb": machine.NewCPUSet(3, 4)}
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", targets))
	groups, err := manager.ListMonGroupCPULists(context.Background(), "dedicated")
	require.NoError(t, err)
	require.Equal(t, []MonGroupCPUList{{Name: "poda", CPUs: targets["poda"]}, {Name: "podb", CPUs: targets["podb"]}}, groups)
	// The write hook verifies no repeated write independently of file permissions.
	manager.writeMonMask = func(*os.File, string) (int, error) {
		return 0, errors.New("unexpected unchanged write")
	}
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", targets))
}

func TestMonGroupManagerPreflightRejectsOverlapAndParentViolation(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000000")
	addMonGroup(t, root, "external", "00000002")
	for _, target := range []machine.CPUSet{machine.NewCPUSet(1), machine.NewCPUSet(5)} {
		require.Error(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", map[string]machine.CPUSet{"poda": target}))
		data, err := os.ReadFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "poda", cpus))
		require.NoError(t, err)
		require.Equal(t, "00000000", string(data))
	}
}

func TestMonGroupManagerDoesNotCreateMissingGroupOrAcceptTraversal(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000000")
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", map[string]machine.CPUSet{"missing": machine.NewCPUSet(1)}))
	require.NoDirExists(t, filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "missing"))
	for _, name := range []string{"..", "../poda", "a/b", "/absolute"} {
		require.Error(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", map[string]machine.CPUSet{name: machine.NewCPUSet(1)}))
	}
}

func TestMonGroupManagerRepairsDriftAndRecreatedGroup(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000000")
	targets := map[string]machine.CPUSet{"poda": machine.NewCPUSet(1)}
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", targets))
	path := filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "poda")
	require.NoError(t, os.WriteFile(filepath.Join(path, cpus), []byte("00000004"), 0o644))
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", targets))
	require.NoError(t, os.RemoveAll(path))
	addMonGroup(t, root, "poda", "00000000")
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", targets))
	got, err := readCPUListMask(filepath.Join(path, cpus))
	require.NoError(t, err)
	require.Equal(t, targets["poda"], got)
}

func TestMonGroupManagerExchangesDisjointOwners(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000002")
	addMonGroup(t, root, "podb", "00000004")
	// Emulate the kernel: a non-empty write removes those CPUs from siblings.
	var writes []string
	manager.writeMonMask = func(file *os.File, mask string) (int, error) {
		writes = append(writes, filepath.Base(filepath.Dir(file.Name()))+"="+mask)
		n, err := file.WriteString(mask)
		if err != nil {
			return n, err
		}
		assigned, err := readCPUListMask(file.Name())
		if err != nil {
			return n, err
		}
		for _, name := range []string{"poda", "podb"} {
			path := filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name, cpus)
			if path == file.Name() {
				continue
			}
			current, err := readCPUListMask(path)
			if err != nil {
				return n, err
			}
			otherMask, err := manager.formatCPUListMask(current.Difference(assigned).String())
			if err != nil {
				return n, err
			}
			if err := os.WriteFile(path, []byte(otherMask), 0o644); err != nil {
				return n, err
			}
		}
		return n, nil
	}
	targets := map[string]machine.CPUSet{"poda": machine.NewCPUSet(2), "podb": machine.NewCPUSet(1)}
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", targets))
	require.Equal(t, []string{"poda=00000004", "podb=00000002"}, writes)
	for name, target := range targets {
		got, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name, cpus))
		require.NoError(t, err)
		require.Equal(t, target, got)
	}
}

func TestMonGroupManagerWriteFailureRetriesWithoutRecreating(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000004")
	targets := map[string]machine.CPUSet{"poda": machine.NewCPUSet(1)}
	manager.writeMonMask = func(file *os.File, mask string) (int, error) {
		if mask != "00000000" {
			return 0, errors.New("injected write failure")
		}
		return file.WriteString(mask)
	}
	require.ErrorContains(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", targets), "injected write failure")
	manager.writeMonMask = nil
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", targets))
}

func TestMonGroupManagerDetectsReplacementDuringWrite(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000004")
	path := filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "poda")
	replaced := false
	manager.writeMonMask = func(file *os.File, mask string) (int, error) {
		if !replaced {
			replaced = true
			if err := os.RemoveAll(path); err != nil {
				return 0, err
			}
			addMonGroup(t, root, "poda", "00000008")
		}
		return file.WriteString(mask)
	}
	err := manager.ApplyMonGroupCPULists(context.Background(), "dedicated", map[string]machine.CPUSet{"poda": machine.NewCPUSet(1)})
	require.Error(t, err)
	actual, err := readCPUListMask(filepath.Join(path, cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(3), actual, "held descriptor must not redirect writes to replacement")
}

func TestMonGroupManagerCancellationBeforeWrite(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000004")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := manager.ApplyMonGroupCPULists(ctx, "dedicated", map[string]machine.CPUSet{"poda": machine.NewCPUSet(1)})
	require.ErrorIs(t, err, context.Canceled)
	actual, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "poda", cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(2), actual)
}

func TestCPUListManagerSkipsSameParentMaskWithMonGroups(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "external", "00000002")
	manager.writeFile = func(string, []byte, os.FileMode) error {
		return errors.New("unexpected same-parent write")
	}
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-4"))
}

func TestCPUListManagerRetriesIncompleteParentRestore(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "external", "00000002")
	addMonGroup(t, root, "poda", "00000004")
	parentWrites := 0
	manager.writeFile = func(path string, data []byte, perm os.FileMode) error {
		parentWrites++
		if err := os.WriteFile(path, data, perm); err != nil {
			return err
		}
		for _, name := range []string{"external", "poda"} {
			if err := os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name, cpus), []byte("00000000"), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	manager.writeMonMask = func(*os.File, string) (int, error) {
		return 0, errors.New("restore denied")
	}
	require.ErrorContains(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"), "restore denied")
	manager.writeMonMask = nil
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"))
	require.Equal(t, 1, parentWrites, "retry must not clear ownership by rewriting the parent")
	groups, err := manager.ListMonGroupCPULists(context.Background(), "dedicated")
	require.NoError(t, err)
	require.Equal(t, []MonGroupCPUList{
		{Name: "external", CPUs: machine.NewCPUSet(1)},
		{Name: "poda", CPUs: machine.NewCPUSet(2)},
	}, groups)
	require.Error(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated",
		map[string]machine.CPUSet{"poda": machine.NewCPUSet(1)}))
}

func TestCPUListManagerParentRestoreRetainsSurvivingOwners(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			manager, root := monGroupFixture(t)
			addMonGroup(t, root, "external", "00000002")
			addMonGroup(t, root, "poda", "00000004")
			manager.writeFile = func(path string, data []byte, perm os.FileMode) error {
				if err := os.WriteFile(path, data, perm); err != nil {
					return err
				}
				for _, name := range []string{"external", "poda"} {
					if err := os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name, cpus), []byte("00000000"), 0o644); err != nil {
						return err
					}
				}
				return nil
			}
			manager.writeMonMask = func(*os.File, string) (int, error) { return 0, errors.New("restore denied") }
			require.Error(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"))
			value, pending := manager.restores.Load("dedicated")
			require.True(t, pending)
			held := value.(*parentMonRestore).snapshot
			require.NoError(t, os.RemoveAll(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "poda")))
			if replacement {
				addMonGroup(t, root, "poda", "00000008")
			}
			manager.writeMonMask = nil
			require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"))
			_, err := held.file.Stat()
			require.Error(t, err)
			require.Equal(t, ^uintptr(0), held.file.Fd(), "retired descriptor must be closed")
			_, pending = manager.restores.Load("dedicated")
			require.False(t, pending)
			got, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus))
			require.NoError(t, err)
			require.Equal(t, machine.NewCPUSet(1), got)
			if replacement {
				got, err = readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "poda", cpus))
				require.NoError(t, err)
				require.Equal(t, machine.NewCPUSet(3), got, "old ownership must not be restored into a replacement")
			}
		})
	}
}

func TestCPUListManagerParentRestoreRetriesPartialCompletion(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "external", "00000002")
	addMonGroup(t, root, "poda", "00000004")
	manager.writeFile = func(path string, data []byte, perm os.FileMode) error {
		if err := os.WriteFile(path, data, perm); err != nil {
			return err
		}
		for _, name := range []string{"external", "poda"} {
			if err := os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name, cpus), []byte("00000000"), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	writes := 0
	manager.writeMonMask = func(file *os.File, mask string) (int, error) {
		writes++
		if writes == 2 {
			return 0, errors.New("second restore denied")
		}
		return file.WriteString(mask)
	}
	require.ErrorContains(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"), "second restore denied")
	matches, err := manager.CPUListMatches(context.Background(), "dedicated", "1-5")
	require.NoError(t, err)
	require.False(t, matches)
	_, err = manager.ListMonGroupCPULists(context.Background(), "dedicated")
	require.ErrorContains(t, err, "incomplete")
	manager.writeMonMask = nil
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", nil))
	groups, err := manager.ListMonGroupCPULists(context.Background(), "dedicated")
	require.NoError(t, err)
	require.Equal(t, []MonGroupCPUList{{Name: "external", CPUs: machine.NewCPUSet(1)}, {Name: "poda", CPUs: machine.NewCPUSet(2)}}, groups)
}

func TestCPUListManagerRetiresPendingRestoreOnCLOSRemoval(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			manager, root := monGroupFixture(t)
			addMonGroup(t, root, "external", "00000002")
			manager.writeFile = func(path string, data []byte, perm os.FileMode) error {
				if err := os.WriteFile(path, data, perm); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus), []byte("00000000"), 0o644)
			}
			manager.writeMonMask = func(*os.File, string) (int, error) { return 0, errors.New("restore denied") }
			require.Error(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"))
			value, pending := manager.restores.Load("dedicated")
			require.True(t, pending)
			held := value.(*parentMonRestore).snapshot
			require.NoError(t, os.RemoveAll(filepath.Join(root, "dedicated")))
			if replacement {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), []byte("0000001e"), 0o644))
				addMonGroup(t, root, "external", "00000008")
			}
			_, err := manager.ListManagedClos(context.Background())
			require.NoError(t, err)
			_, pending = manager.restores.Load("dedicated")
			require.False(t, pending)
			_, err = held.file.Stat()
			require.Error(t, err)
			require.Equal(t, ^uintptr(0), held.file.Fd(), "retired descriptor must be closed")
			if replacement {
				got, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus))
				require.NoError(t, err)
				require.Equal(t, machine.NewCPUSet(3), got)
			}
		})
	}
}

func TestCPUListManagerPendingRestoreRejectsExternalDrift(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "external", "00000002")
	manager.writeFile = func(path string, data []byte, perm os.FileMode) error {
		if err := os.WriteFile(path, data, perm); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus), []byte("00000000"), 0o644)
	}
	manager.writeMonMask = func(*os.File, string) (int, error) { return 0, errors.New("restore denied") }
	require.Error(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, manager.ApplyCPUList(ctx, "dedicated", "1-5"), context.Canceled)
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus), []byte("00000008"), 0o644))
	manager.writeMonMask = nil
	require.ErrorContains(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"), "changed during incomplete restore")
	got, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(3), got)
	value, pending := manager.restores.LoadAndDelete("dedicated")
	require.True(t, pending)
	value.(*parentMonRestore).snapshot.close()
}

func TestMonCPUListSnapshotRejectsMaskGrowth(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000000")
	snapshot, err := manager.snapshotMonCPULists(context.Background(), "dedicated")
	require.NoError(t, err)
	defer snapshot.close()
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "poda", cpus), []byte("00000000,00000000"), 0o644))
	require.Error(t, manager.checkMonCPUListSnapshot(context.Background(), "dedicated", snapshot))
}

func TestCPUListManagerRestoresMonGroupsAfterParentChange(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "external", "00000002")
	manager.writeFile = func(path string, data []byte, perm os.FileMode) error {
		if err := os.WriteFile(path, data, perm); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus), []byte("00000000"), 0o644)
	}
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"))
	actual, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(1), actual)
}

func TestMonGroupManagerFailedWidenKeepsExistingOwnership(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000006")
	manager.writeMonMask = func(file *os.File, mask string) (int, error) {
		if mask == "00000000" {
			return file.WriteString(mask)
		}
		return 0, errors.New("injected assignment failure")
	}
	require.ErrorContains(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated",
		map[string]machine.CPUSet{"poda": machine.NewCPUSet(1, 2, 3)}), "injected assignment failure")
	actual, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "poda", cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(1, 2), actual)
}

func TestMonGroupManagerRejectsReplacementWhileOpening(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000000")
	path := filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "poda")
	manager.openMonFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		require.NoError(t, os.RemoveAll(path))
		addMonGroup(t, root, "poda", "00000004")
		return os.OpenFile(name, flag, perm)
	}
	require.Error(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated",
		map[string]machine.CPUSet{"poda": machine.NewCPUSet(1)}))
	actual, err := readCPUListMask(filepath.Join(path, cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(2), actual)
}

func TestMonGroupManagerRejectsNewSiblingDuringSnapshot(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000000")
	manager.openMonFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		addMonGroup(t, root, "external", "00000002")
		return os.OpenFile(name, flag, perm)
	}
	require.Error(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated",
		map[string]machine.CPUSet{"poda": machine.NewCPUSet(1)}))
	actual, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(1), actual)
}

func TestCPUListManagerClipsMonOwnershipWhenParentShrinks(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "external", "0000000a")
	manager.writeFile = func(path string, data []byte, perm os.FileMode) error {
		if err := os.WriteFile(path, data, perm); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus), []byte("00000000"), 0o644)
	}
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-2"))
	actual, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(1), actual)
}

func TestCPUListManagerParentResetClearsAllMonOwnership(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000002")
	addMonGroup(t, root, "external", "00000004")
	manager.writeFile = func(path string, data []byte, perm os.FileMode) error {
		if err := os.WriteFile(path, data, perm); err != nil {
			return err
		}
		for _, name := range []string{"poda", "external"} {
			if err := os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name, cpus), []byte("00000000"), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", ""))
	for _, name := range []string{"poda", "external"} {
		actual, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name, cpus))
		require.NoError(t, err)
		require.True(t, actual.IsEmpty())
	}
}

func TestMonGroupManagerUntouchedOwnerDriftStopsRemainingWrites(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "external", "00000008")
	addMonGroup(t, root, "poda", "00000000")
	addMonGroup(t, root, "podb", "00000000")
	writes := 0
	manager.writeMonMask = func(file *os.File, mask string) (int, error) {
		writes++
		n, err := file.WriteString(mask)
		if err != nil {
			return n, err
		}
		return n, os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus), []byte("00000004"), 0o644)
	}
	require.Error(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated",
		map[string]machine.CPUSet{"poda": machine.NewCPUSet(1), "podb": machine.NewCPUSet(2)}))
	require.Equal(t, 1, writes)
	actual, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(2), actual)
}

func TestCPUListManagerParentThenMonKeepsUnknownOwner(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000000")
	addMonGroup(t, root, "external", "00000002")
	manager.writeFile = func(path string, data []byte, perm os.FileMode) error {
		if err := os.WriteFile(path, data, perm); err != nil {
			return err
		}
		for _, name := range []string{"poda", "external"} {
			if err := os.WriteFile(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name, cpus), []byte("00000000"), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-5"))
	groups, err := manager.ListMonGroupCPULists(context.Background(), "dedicated")
	require.NoError(t, err)
	require.Equal(t, []MonGroupCPUList{
		{Name: "external", CPUs: machine.NewCPUSet(1)},
		{Name: "poda", CPUs: machine.NewCPUSet()},
	}, groups)
	// A Pod cannot claim the preserved external CPU after the parent write.
	require.Error(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated",
		map[string]machine.CPUSet{"poda": machine.NewCPUSet(1)}))
	require.NoError(t, manager.ApplyMonGroupCPULists(context.Background(), "dedicated",
		map[string]machine.CPUSet{"poda": machine.NewCPUSet(2)}))
	actual, err := readCPUListMask(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, "external", cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(1), actual)
}

func TestCPUListManagerDeletedMonIsNotSuccessfulReset(t *testing.T) {
	manager, root := monGroupFixture(t)
	addMonGroup(t, root, "poda", "00000002")
	manager.openMonFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		require.NoError(t, os.RemoveAll(filepath.Dir(name)))
		return os.OpenFile(name, flag, perm)
	}
	require.Error(t, manager.ApplyCPUList(context.Background(), "dedicated", ""))
	actual, err := readCPUListMask(filepath.Join(root, "dedicated", cpus))
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(1, 2, 3, 4), actual)
}

func BenchmarkMonGroupCPUListAssignments(b *testing.B) {
	for _, count := range []int{16, 64, 128} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			root := b.TempDir()
			const full = "ffffffff,ffffffff,ffffffff,ffffffff"
			const empty = "00000000,00000000,00000000,00000000"
			require.NoError(b, os.WriteFile(filepath.Join(root, cpus), []byte(full), 0o644))
			require.NoError(b, os.MkdirAll(filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir), 0o755))
			require.NoError(b, os.WriteFile(filepath.Join(root, "dedicated", cpus), []byte(full), 0o644))
			targets := make(map[string]machine.CPUSet, count)
			paths := make([]string, 0, count)
			for i := 0; i < count; i++ {
				name := fmt.Sprintf("pod%04d", i)
				dir := filepath.Join(root, "dedicated", resctrlutil.MonGroupsDir, name)
				require.NoError(b, os.Mkdir(dir, 0o755))
				path := filepath.Join(dir, cpus)
				require.NoError(b, os.WriteFile(path, []byte(empty), 0o644))
				paths = append(paths, path)
				targets[name] = machine.NewCPUSet(i)
			}
			manager := newCPUListManagerWithUpdater(root, nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				for _, path := range paths {
					require.NoError(b, os.WriteFile(path, []byte(empty), 0o644))
				}
				b.StartTimer()
				require.NoError(b, manager.ApplyMonGroupCPULists(context.Background(), "dedicated", targets))
			}
		})
	}
}

func TestReadCPUListMaskRejectsMalformedAndPreservesWordOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), cpus)
	require.NoError(t, os.WriteFile(path, []byte("00000002,80000001\n"), 0o644))
	got, err := readCPUListMask(path)
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(0, 31, 33), got)
	for _, bad := range []string{"", "garbage", "100000000", "2,,1"} {
		require.NoError(t, os.WriteFile(path, []byte(bad), 0o644))
		_, err := readCPUListMask(path)
		require.Error(t, err)
	}
}
