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
	"bytes"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"
)

type testLogging struct{}

func (_ testLogging) log(message string, params ...interface{}) string {
	return logging(message, params...)
}

func (_ testLogging) logPath(pkg LoggingPKG, message string, params ...interface{}) string {
	return loggingPath(pkg, message, params...)
}

type testLogger struct {
	klog Logger // klog for katalyst-log
}

func (t testLogger) log(message string, params ...interface{}) string {
	return t.klog.logging(message, params...)
}

func TestLogging(t *testing.T) {
	t.Parallel()

	loggingWithoutStruct := logging("extra %v %v", 1, "test")
	require.Equal(t, "[testing.tRunner] extra 1 test", loggingWithoutStruct)

	loggingWithStruct := testLogging{}.log("extra %v %v", 1, "test")
	require.Equal(t, "[katalyst-core/pkg/util/general.TestLogging] extra 1 test", loggingWithStruct)

	loggingPathWithStruct := testLogging{}.logPath(LoggingPKGShort, "extra %v %v", 1, "test")
	require.Equal(t, "[general.TestLogging] extra 1 test", loggingPathWithStruct)

	loggerWithoutStruct := LoggerWithPrefix("p-test", LoggingPKGNone).logging("extra %v %v", 1, "test")
	require.Equal(t, "[p-test: tRunner] extra 1 test", loggerWithoutStruct)

	loggerWithoutStruct = LoggerWithPrefix("p-test", LoggingPKGShort).logging("extra %v %v", 1, "test")
	require.Equal(t, "[p-test: testing.tRunner] extra 1 test", loggerWithoutStruct)

	loggerWithoutStruct = LoggerWithPrefix("p-test", LoggingPKGFull).logging("extra %v %v", 1, "test")
	require.Equal(t, "[p-test: testing.tRunner] extra 1 test", loggerWithoutStruct)

	loggerWithStruct := testLogger{klog: LoggerWithPrefix("p-test", LoggingPKGNone)}.log("extra %v %v", 1, "test")
	require.Equal(t, "[p-test: TestLogging] extra 1 test", loggerWithStruct)

	loggerWithStruct = testLogger{klog: LoggerWithPrefix("p-test", LoggingPKGShort)}.log("extra %v %v", 1, "test")
	require.Equal(t, "[p-test: general.TestLogging] extra 1 test", loggerWithStruct)

	loggerWithStruct = testLogger{klog: LoggerWithPrefix("p-test", LoggingPKGFull)}.log("extra %v %v", 1, "test")
	require.Equal(t, "[p-test: katalyst-core/pkg/util/general.TestLogging] extra 1 test", loggerWithStruct)

	InfoS("test-InfoS", "param-key", "param-InfoS")
	Infof("test-Infof %v", "extra-Infof")
	InfofV(1, "test-InfofV %v", "extra-InfofV")
	Warningf("test-Warningf %v", "extra-Warningf")
	Errorf("test-Errorf %v", "extra-Errorf")
	ErrorS(fmt.Errorf("err"), "test-ErrorS", "param-key", "param-ErrorS")

	InfoSPath(LoggingPKGShort, "test-InfoS", "param-key", "param-InfoS")
	InfofPath(LoggingPKGShort, "test-Infof %v", "extra-Infof")
	InfofVPath(LoggingPKGShort, 1, "test-InfofV %v", "extra-InfofV")
	WarningfPath(LoggingPKGShort, "test-Warningf %v", "extra-Warningf")
	ErrorfPath(LoggingPKGShort, "test-Errorf %v", "extra-Errorf")
	ErrorSPath(LoggingPKGShort, fmt.Errorf("err"), "test-ErrorS", "param-key", "param-ErrorS")

	go func() {
		goStr := logging("extra %v %v", 1, "test")
		require.Equal(t, "[runtime.goexit] extra 1 test", goStr)
	}()

	f := func() {
		funcStr := logging("extra %v %v", 1, "test")
		require.Equal(t, "[runtime.goexit] extra 1 test", funcStr)
	}
	go f()

	time.Sleep(time.Millisecond)
}

func TestInfoSVDoesNotPanic(t *testing.T) {
	t.Parallel()

	// InfoSV must stay safe both when the level is disabled (default verbosity)
	// and when it is enabled; it lazy-gates internally and keeps key/value shape.
	InfoSV(4, "test-InfoSV-disabled", "scope", "s1")
	InfoSV(0, "test-InfoSV-enabled", "scope", "s2", "n", 1)
}

// TestInfoSVCapturesMessageAndKeyValues redirects klog to a buffer and asserts
// the InfoSV message and its structured key/value pairs actually reach the log
// sink at an enabled verbosity, while staying silent when the level is disabled.
func TestInfoSVCapturesMessageAndKeyValues(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	klog.SetOutput(&buf)
	defer klog.SetOutput(os.Stderr)
	klog.InitFlags(flag.CommandLine)
	require.NoError(t, flag.Set("v", "2"))
	require.NoError(t, flag.Set("logtostderr", "false"))
	defer func() { _ = flag.Set("v", "0"); _ = flag.Set("logtostderr", "true") }()

	// disabled level (5 > 2): must emit nothing.
	buf.Reset()
	InfoSV(5, "probe-disabled", "k", "v")
	klog.Flush()
	require.NotContains(t, buf.String(), "probe-disabled")

	// enabled level (2 <= 2): message + structured kvs reach the sink.
	InfoSV(2, "probe-enabled", "probe-key", "probe-value")
	klog.Flush()
	out := buf.String()
	require.Contains(t, out, "probe-enabled")
	require.Contains(t, out, "probe-key")
	require.Contains(t, out, "probe-value")
}
