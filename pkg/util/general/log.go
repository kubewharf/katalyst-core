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
	"strconv"
	"strings"
	"sync"

	"k8s.io/klog/v2"
)

type LoggingPKG int

func (l *LoggingPKG) Type() string {
	return "LoggingPKG"
}

func (l *LoggingPKG) String() string {
	return fmt.Sprintf("logging-level: %v", *l)
}

func (l *LoggingPKG) Set(value string) error {
	level, err := strconv.Atoi(value)
	if err != nil {
		return err
	}
	*l = LoggingPKG(level)
	return nil
}

const (
	LoggingPKGNone LoggingPKG = iota
	LoggingPKGShort
	LoggingPKGFull
)

var (
	defaultLoggingPackage = LoggingPKGFull
	defaultLoggingMtx     sync.RWMutex
)

// SetDefaultLoggingPackage should only be called by flags,
// and should not be alerted dynamically.
func SetDefaultLoggingPackage(l LoggingPKG) {
	defaultLoggingMtx.Lock()
	defer defaultLoggingMtx.Unlock()
	defaultLoggingPackage = l
}

func getDefaultLoggingPackage() LoggingPKG {
	defaultLoggingMtx.RLock()
	defer defaultLoggingMtx.RUnlock()
	return defaultLoggingPackage
}

const callDepth = 3

const skippedPackagePrefix = "github.com/kubewharf/"

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
		return strings.TrimPrefix(callPath, skippedPackagePrefix)
	}
	return ""
}

func logging(message string, params ...interface{}) string {
	return "[" + loggingWithDepth(getDefaultLoggingPackage()) + "] " + fmt.Sprintf(message, params...)
}

func loggingPath(pkg LoggingPKG, message string, params ...interface{}) string {
	return "[" + loggingWithDepth(pkg) + "] " + fmt.Sprintf(message, params...)
}

func InfoS(message string, params ...interface{}) {
	klog.InfoSDepth(1, logging(message), params...)
}

func InfoSPath(pkg LoggingPKG, message string, params ...interface{}) {
	klog.InfoSDepth(1, loggingPath(pkg, message), params...)
}

func Infof(message string, params ...interface{}) {
	klog.InfofDepth(1, logging(message, params...))
}

func InfofPath(pkg LoggingPKG, message string, params ...interface{}) {
	klog.InfofDepth(1, loggingPath(pkg, message, params...))
}

func InfofV(level int, message string, params ...interface{}) {
	klog.V(klog.Level(level)).InfofDepth(1, logging(message, params...))
}

func InfofVPath(pkg LoggingPKG, level int, message string, params ...interface{}) {
	klog.V(klog.Level(level)).InfofDepth(1, loggingPath(pkg, message, params...))
}

func Warningf(message string, params ...interface{}) {
	klog.WarningfDepth(1, logging(message, params...))
}

func WarningfPath(pkg LoggingPKG, message string, params ...interface{}) {
	klog.WarningfDepth(1, loggingPath(pkg, message, params...))
}

func Errorf(message string, params ...interface{}) {
	klog.ErrorfDepth(1, logging(message, params...))
}

func ErrorfPath(pkg LoggingPKG, message string, params ...interface{}) {
	klog.ErrorfDepth(1, loggingPath(pkg, message, params...))
}

func ErrorS(err error, message string, params ...interface{}) {
	klog.ErrorSDepth(1, err, logging(message), params...)
}

func ErrorSPath(pkg LoggingPKG, err error, message string, params ...interface{}) {
	klog.ErrorSDepth(1, err, loggingPath(pkg, message), params...)
}

func Fatalf(message string, params ...interface{}) {
	klog.FatalfDepth(1, logging(message, params...))
}

func FatalfPath(pkg LoggingPKG, message string, params ...interface{}) {
	klog.FatalfDepth(1, loggingPath(pkg, message, params...))
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
