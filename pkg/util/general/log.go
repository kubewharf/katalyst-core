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
	"fmt"
	"runtime"
	"strings"

	"k8s.io/klog/v2"
)

const callDepth = 2

// loggingWithDepth returns the logging-prefix for caller.
// it will help to avoid hardcode function names in logging
// message especially for cases that function names are changed.
func loggingWithDepth() string {
	pc, _, _, ok := runtime.Caller(callDepth)
	if !ok {
		return ""
	}

	funcPaths := strings.Split(runtime.FuncForPC(pc).Name(), "/")
	if len(funcPaths) == 0 {
		return ""
	}

	funcNames := strings.Split(funcPaths[len(funcPaths)-1], ".")
	switch len(funcNames) {
	case 2:
		return fmt.Sprintf("[pkg: %v, func: %v]", funcNames[0], funcNames[1])
	case 3:
		return fmt.Sprintf("[pkg: %v, obj: %v, func: %v]", funcNames[0], funcNames[1], funcNames[2])
	}

	return ""
}

func logging(message string, params ...interface{}) string {
	return loggingWithDepth() + " " + fmt.Sprintf(message, params...)
}

func InfoS(message string, params ...interface{}) {
	klog.InfoSDepth(1, logging(message), params...)
}

func Infof(message string, params ...interface{}) {
	klog.InfofDepth(1, logging(message, params...))
}

func InfofV(level int, message string, params ...interface{}) {
	klog.V(klog.Level(level)).InfofDepth(1, logging(message, params...))
}

func Warningf(message string, params ...interface{}) {
	klog.WarningfDepth(1, logging(message, params...))
}

func Errorf(message string, params ...interface{}) {
	klog.ErrorfDepth(1, logging(message, params...))
}

func ErrorS(err error, message string, params ...interface{}) {
	klog.ErrorSDepth(1, err, logging(message), params...)
}
