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

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCATCapabilityProviderReadsL3MaskForEverySchemataDomain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "cbm_mask"), []byte("ff\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "min_cbm_bits"), []byte("2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "schemata"), []byte("L3:0=ff;2=ff;\nMB:0=100;2=100;\n"), 0o644))

	capabilities, err := newCATCapabilityProvider(root).GetCATCapabilities()

	require.NoError(t, err)
	require.Equal(t, map[int]CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 2},
		2: {CBMMask: 0xff, MinCBMBits: 2},
	}, capabilities)
}

func TestCATCapabilityProviderReadsIndentedL3Schemata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "cbm_mask"), []byte("7ff\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "min_cbm_bits"), []byte("1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "schemata"), []byte("    L3:0=7ff;1=7ff\n    MB:0=100;1=100\n"), 0o644))

	capabilities, err := newCATCapabilityProvider(root).GetCATCapabilities()

	require.NoError(t, err)
	require.Equal(t, map[int]CATCapability{
		0: {CBMMask: 0x7ff, MinCBMBits: 1},
		1: {CBMMask: 0x7ff, MinCBMBits: 1},
	}, capabilities)
}

func TestCATCapabilityProviderAcceptsZeroMinCBMBits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "cbm_mask"), []byte("ffff\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "min_cbm_bits"), []byte("0\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "schemata"), []byte("L3:0=ffff;1=ffff;\n"), 0o644))

	capabilities, err := newCATCapabilityProvider(root).GetCATCapabilities()

	require.NoError(t, err)
	require.Equal(t, map[int]CATCapability{
		0: {CBMMask: 0xffff, MinCBMBits: 0},
		1: {CBMMask: 0xffff, MinCBMBits: 0},
	}, capabilities)
}

func TestCATCapabilityProviderReadsBitUsageRightmostCharacterAsLowestBit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "cbm_mask"), []byte("f\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "min_cbm_bits"), []byte("1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "bit_usage"), []byte("0=XXSS;1=SSXX\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "schemata"), []byte("L3:0=f;1=f;\n"), 0o644))

	capabilities, err := newCATCapabilityProvider(root).GetCATCapabilities()

	require.NoError(t, err)
	require.Equal(t, map[int]CATCapability{
		0: {CBMMask: 0xf, MinCBMBits: 1, BitUsageByType: map[string]uint64{"S": 0x3, "X": 0xc}},
		1: {CBMMask: 0xf, MinCBMBits: 1, BitUsageByType: map[string]uint64{"S": 0xc, "X": 0x3}},
	}, capabilities)
}

func TestCATCapabilityProviderIgnoresMissingBitUsage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "cbm_mask"), []byte("f\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "min_cbm_bits"), []byte("1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "schemata"), []byte("L3:0=f;\n"), 0o644))

	capabilities, err := newCATCapabilityProvider(root).GetCATCapabilities()

	require.NoError(t, err)
	require.Equal(t, map[int]CATCapability{
		0: {CBMMask: 0xf, MinCBMBits: 1},
	}, capabilities)
}

func TestCATCapabilityProviderRejectsMalformedBitUsage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "cbm_mask"), []byte("f\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "min_cbm_bits"), []byte("1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "bit_usage"), []byte("0XXSS\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "schemata"), []byte("L3:0=f;\n"), 0o644))

	_, err := newCATCapabilityProvider(root).GetCATCapabilities()

	require.Error(t, err)
	require.Contains(t, err.Error(), "bit_usage")
}

func TestCATCapabilityProviderRejectsMalformedDomain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "cbm_mask"), []byte("ff\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "min_cbm_bits"), []byte("1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "schemata"), []byte("L3:invalid=ff;\n"), 0o644))

	_, err := newCATCapabilityProvider(root).GetCATCapabilities()

	require.Error(t, err)
	require.Contains(t, err.Error(), "L3 domain")
}

func TestCATCapabilityProviderRejectsCDPWithoutSideEffects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3CODE"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3DATA"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3CODE", "cbm_mask"), []byte("ff\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3DATA", "cbm_mask"), []byte("ff\n"), 0o644))

	_, err := newCATCapabilityProvider(root).GetCATCapabilities()

	require.Error(t, err)
	require.Contains(t, err.Error(), "L3CODE/L3DATA")
}

func TestCATCapabilityProviderReportsMissingResctrlAsUnsupported(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		filepath.Join("info", "L3", "cbm_mask"),
		filepath.Join("info", "L3"),
		"info",
		".",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if path != "." {
				require.NoError(t, os.MkdirAll(filepath.Join(root, path), 0o755))
			}

			_, err := newCATCapabilityProvider(filepath.Join(root, path)).GetCATCapabilities()

			require.ErrorIs(t, err, ErrCATUnsupported)
		})
	}
}

func TestCATCapabilityProviderReportsMissingRequiredL3FilesAsUnsupported(t *testing.T) {
	t.Parallel()
	for _, missingPath := range []string{
		filepath.Join("info", "L3", "min_cbm_bits"),
		schemataFile,
	} {
		missingPath := missingPath
		t.Run(missingPath, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "cbm_mask"), []byte("ff\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "min_cbm_bits"), []byte("1\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(root, schemataFile), []byte("L3:0=ff;\n"), 0o644))
			require.NoError(t, os.Remove(filepath.Join(root, missingPath)))

			_, err := newCATCapabilityProvider(root).GetCATCapabilities()

			require.ErrorIs(t, err, ErrCATUnsupported)
		})
	}
}

func TestCATCapabilityProviderPreservesCapabilityReadErrors(t *testing.T) {
	t.Parallel()
	for _, deniedPath := range []string{
		filepath.Join("info", "L3", "min_cbm_bits"),
		schemataFile,
	} {
		deniedPath := deniedPath
		t.Run(deniedPath, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "info", "L3"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "cbm_mask"), []byte("ff\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(root, "info", "L3", "min_cbm_bits"), []byte("1\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(root, schemataFile), []byte("L3:0=ff;\n"), 0o644))
			provider := newCATCapabilityProvider(root)
			provider.readFile = func(path string) ([]byte, error) {
				if path == filepath.Join(root, deniedPath) {
					return nil, fs.ErrPermission
				}
				return os.ReadFile(path)
			}

			_, err := provider.GetCATCapabilities()

			require.ErrorIs(t, err, fs.ErrPermission)
			require.False(t, errors.Is(err, ErrCATUnsupported))
		})
	}
}
