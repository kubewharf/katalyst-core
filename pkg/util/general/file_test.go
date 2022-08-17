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

package general

import (
	"testing"
	"time"
)

func Test_fileUniqueLock(t *testing.T) {
	lockPath := "/tmp/test_lock"

	flock, err := GetUniqueLock(lockPath)
	if err != nil {
		t.Errorf("GetUniqueLock() error = %v, wantErr %v", err, nil)
		return
	}

	_, err = GetUniqueLockWithTimeout(lockPath, time.Second, 3)
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	ReleaseUniqueLock(flock)
	flock, err = GetUniqueLock(lockPath)
	if err != nil {
		t.Errorf("GetUniqueLock() error = %v, wantErr %v", err, nil)
		return
	}
	ReleaseUniqueLock(flock)
}
