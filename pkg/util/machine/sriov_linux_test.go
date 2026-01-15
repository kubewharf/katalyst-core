//go:build linux
// +build linux

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
	"strings"
	"testing"

	. "github.com/bytedance/mockey"
	. "github.com/smartystreets/goconvey/convey"
)

func TestIsSriovPf(t *testing.T) {
	PatchConvey("TestIsSriovPf", t, func() {
		PatchConvey("is pf", func() {
			Mock(os.Stat).Return(nil, os.ErrNotExist).Build()
			res := isSriovPf("/sys", "eth0")
			So(res, ShouldBeFalse)
		})

		PatchConvey("is not pf", func() {
			Mock(os.Stat).
				When(func(name string) bool { return strings.HasSuffix(name, netFileNamePhysfn) }).Return(nil, nil).
				When(func(name string) bool { return strings.Contains(name, netFileNameNumVFS) }).Return(nil, nil).
				Build()
			res := isSriovPf("/sys", "eth0")
			So(res, ShouldBeTrue)
		})
	})
}
