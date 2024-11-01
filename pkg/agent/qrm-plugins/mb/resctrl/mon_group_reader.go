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

package resctrl

import (
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type MonGroupReader interface {
	ReadMB(monGroup string, dies []int) (map[int]int, error)
}

type monGroupReader struct {
	ccdReader CCDMBReader
}

func (m monGroupReader) ReadMB(monGroup string, dies []int) (map[int]int, error) {
	result := make(map[int]int)
	for _, ccd := range dies {
		mb, err := m.ccdReader.ReadMB(monGroup, ccd)
		if err != nil {
			general.InfofV(6, "mbm: failed to get mb for mon group %s, ccd %d", monGroup, ccd)
			continue
		}
		result[ccd] = mb
	}
	return result, nil
}

func NewMonGroupReader(ccdReader CCDMBReader) (MonGroupReader, error) {
	return &monGroupReader{
		ccdReader: ccdReader,
	}, nil
}
