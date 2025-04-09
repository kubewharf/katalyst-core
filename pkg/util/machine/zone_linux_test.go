//go:build linux

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
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetNormalZoneInfo(t *testing.T) {
	t.Parallel()

	rootDir := os.TempDir()
	dir := filepath.Join(rootDir, "tmp")
	err := os.Mkdir(dir, 0o700)
	assert.NoError(t, err)

	tmpDir, err := ioutil.TempDir(dir, "fake-proc")
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	content := "Node 0, zone   Normal\n min      7128\n low      71013\n nr_free_pages 72543\n nr_zone_inactive_file 473057\n"
	statFile := filepath.Join(tmpDir, "zoneinfo")
	err = ioutil.WriteFile(statFile, []byte(content), 0o700)
	assert.NoError(t, err)

	zoneinfo := GetNormalZoneInfo(statFile)
	assert.Equal(t, "72543", fmt.Sprint(zoneinfo[0].Free))
	assert.Equal(t, "7128", fmt.Sprint(zoneinfo[0].Min))
	assert.Equal(t, "71013", fmt.Sprint(zoneinfo[0].Low))
	assert.Equal(t, "473057", fmt.Sprint(zoneinfo[0].FileInactive))
}
