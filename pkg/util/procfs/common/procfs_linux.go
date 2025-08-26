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

package common

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/eventbus"
)

// InstrumentedWriteFileIfChange wraps WriteFileIfChange with audit logic
func InstrumentedWriteFileIfChange(dir, file, data string) (err error, applied bool, oldData string) {
	startTime := time.Now()
	defer func() {
		if applied {
			_ = eventbus.GetDefaultEventBus().Publish(consts.TopicNameApplyProcFS, eventbus.RawProcfsEvent{
				BaseEventImpl: eventbus.BaseEventImpl{
					Time: startTime,
				},
				Cost:     time.Now().Sub(startTime),
				ProcPath: dir,
				ProcFile: file,
				Data:     data,
				OldData:  oldData,
			})
		}
	}()

	err, applied, oldData = writeFileIfChange(dir, file, data)
	return
}

// writeFileIfChange writes data to the procfs joined by dir and
// file if new data is not equal to the old data and return the old data.
func writeFileIfChange(dir, file, data string) (error, bool, string) {
	path := filepath.Join(dir, file)
	oldData, err := os.ReadFile(path)
	if err != nil {
		return err, false, ""
	}
	oldDataStr := string(oldData)

	if strings.TrimSpace(data) != strings.TrimSpace(oldDataStr) {
		if err = os.WriteFile(path, []byte(data), 0o644); err != nil {
			return err, false, oldDataStr
		} else {
			return nil, true, oldDataStr
		}
	}
	return nil, false, oldDataStr
}
