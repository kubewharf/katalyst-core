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

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseIsolCPUSetFromCmdlineFile(t *testing.T) {
	t.Parallel()

	tcs := []struct {
		name        string
		cmdline     string
		expectedSet CPUSet
		expectErr   bool
	}{
		{
			name:        "no isolcpus configured",
			cmdline:     "BOOT_IMAGE=/boot/vmlinuz root=/dev/sda1 ro quiet",
			expectedSet: NewCPUSet(),
		},
		{
			name:        "empty isolcpus value",
			cmdline:     "BOOT_IMAGE=/boot/vmlinuz isolcpus= ro",
			expectedSet: NewCPUSet(),
		},
		{
			name:        "plain cpu list",
			cmdline:     "BOOT_IMAGE=/boot/vmlinuz isolcpus=1-3,5 ro",
			expectedSet: NewCPUSet(1, 2, 3, 5),
		},
		{
			name:        "with known flags before cpu list",
			cmdline:     "BOOT_IMAGE=/boot/vmlinuz isolcpus=nohz,domain,managed_irq,2-4,7 ro",
			expectedSet: NewCPUSet(2, 3, 4, 7),
		},
		{
			name:        "only flags, no cpu list",
			cmdline:     "isolcpus=nohz,domain quiet",
			expectedSet: NewCPUSet(),
		},
		{
			name:        "ignore key=val sub-options",
			cmdline:     "isolcpus=managed_irq,key=val,1-2 ro",
			expectedSet: NewCPUSet(1, 2),
		},
		{
			name:      "invalid cpu list value returns error",
			cmdline:   "isolcpus=abc ro",
			expectErr: true,
		},
		{
			name:        "single cpu",
			cmdline:     "isolcpus=8",
			expectedSet: NewCPUSet(8),
		},
		{
			name:        "consecutive whitespace separators",
			cmdline:     "BOOT_IMAGE=/boot/vmlinuz   isolcpus=1,3   ro",
			expectedSet: NewCPUSet(1, 3),
		},
	}

	for _, tc := range tcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			fakePath := filepath.Join(dir, "cmdline")
			require.NoError(t, os.WriteFile(fakePath, []byte(tc.cmdline+"\n"), 0o644))

			got, err := parseIsolCPUSetFromCmdlineFile(fakePath)
			if tc.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.True(t, tc.expectedSet.Equals(got),
				"case %q: expected %v, got %v", tc.name, tc.expectedSet, got)
		})
	}
}

func TestParseIsolCPUSetFromCmdlineFile_ReadFileError(t *testing.T) {
	t.Parallel()

	got, err := parseIsolCPUSetFromCmdlineFile(filepath.Join(t.TempDir(), "non-existent-cmdline"))
	assert.Error(t, err)
	assert.True(t, got.IsEmpty())
}
