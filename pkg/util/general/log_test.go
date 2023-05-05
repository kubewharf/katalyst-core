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

	"github.com/stretchr/testify/require"
)

type test struct{}

func (_ test) log(message string, params ...interface{}) string {
	return logging(message, params...)
}

func TestLoggingPrefix(t *testing.T) {
	withoutStruct := logging("extra %v %v", 1, "test")
	require.Equal(t, "[pkg: general, func: TestLoggingPrefix] extra 1 test", withoutStruct)

	withStruct := test{}.log("extra %v %v", 1, "test")
	require.Equal(t, "[pkg: general, obj: test, func: log] extra 1 test", withStruct)
}
