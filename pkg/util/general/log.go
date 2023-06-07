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

type LoggingPKG int

const callDepth = 3

const (
	LoggingPKGNone LoggingPKG = iota
	LoggingPKGShort
	LoggingPKGFull
)

// loggingWithDepth returns the logging-prefix for caller.
// it will help to avoid hardcode function names in logging
// message especially for cases that function names are changed.
func loggingWithDepth(pkg LoggingPKG) string {
	pc, _, _, ok := runtime.Caller(callDepth)
	if !ok {
		return ""
	}

	callPath := runtime.FuncForPC(pc).Name()
	callerNames := strings.Split(callPath, "/")
	if len(callerNames) == 0 {
		return ""
	}

	switch pkg {
	case LoggingPKGNone:
		funcNames := strings.Split(callerNames[len(callerNames)-1], ".")
		if len(funcNames) == 0 {
			return ""
		}
		return funcNames[len(funcNames)-1]
	case LoggingPKGShort:
		return callerNames[len(callerNames)-1]
	case LoggingPKGFull:
		return callPath
	}
	return ""
}

func logging(message string, params ...interface{}) string {
	return "[" + loggingWithDepth(LoggingPKGShort) + "] " + fmt.Sprintf(message, params...)
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

func Fatalf(message string, params ...interface{}) {
	klog.FatalfDepth(1, logging(message, params...))
}

type Logger struct {
	pkg    LoggingPKG
	prefix string
}

func LoggerWithPrefix(prefix string, pkg LoggingPKG) Logger {
	if len(prefix) > 0 {
		prefix = fmt.Sprintf("%v: ", prefix)
	}
	return Logger{pkg: pkg, prefix: prefix}
}

func (l Logger) logging(message string, params ...interface{}) string {
	return "[" + l.prefix + loggingWithDepth(l.pkg) + "] " + fmt.Sprintf(message, params...)
}

func (l Logger) InfoS(message string, params ...interface{}) {
	klog.InfoSDepth(1, l.logging(message), params...)
}

func (l Logger) Infof(message string, params ...interface{}) {
	klog.InfofDepth(1, l.logging(message, params...))
}

func (l Logger) InfofV(level int, message string, params ...interface{}) {
	klog.V(klog.Level(level)).InfofDepth(1, l.logging(message, params...))
}

func (l Logger) Warningf(message string, params ...interface{}) {
	klog.WarningfDepth(1, l.logging(message, params...))
}

func (l Logger) Errorf(message string, params ...interface{}) {
	klog.ErrorfDepth(1, l.logging(message, params...))
}

func (l Logger) ErrorS(err error, message string, params ...interface{}) {
	klog.ErrorSDepth(1, err, l.logging(message), params...)
}

func (l Logger) Fatalf(message string, params ...interface{}) {
	klog.FatalfDepth(1, l.logging(message, params...))
}
