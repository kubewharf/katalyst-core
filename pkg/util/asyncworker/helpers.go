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

package asyncworker

import (
	"fmt"
	"reflect"
)

func (s *workStatus) IsWorking() bool {
	return s.working
}

func validateWork(work *Work) (err error) {
	if work == nil {
		return fmt.Errorf("nil work")
	} else if work.Fn == nil {
		return fmt.Errorf("nil work Fn")
	} else if work.DeliveredAt.IsZero() {
		return fmt.Errorf("zero work DeliveredAt")
	}

	fnType := reflect.TypeOf(work.Fn)
	if fnType.Kind() != reflect.Func {
		return fmt.Errorf("work Fn isn't a function")
	} else if fnType.NumOut() != 1 || !fnType.Out(0).Implements(reflect.TypeOf(&err).Elem()) {
		return fmt.Errorf("work Fn doesn't return only an error, numOut: %d", fnType.NumOut())
	}

	return nil
}
