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

package log

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"testing"

	"k8s.io/klog/v2"
)

func TestInitContextAndGetCtxFields(t *testing.T) {
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "case1",
			args: args{
				ctx: context.Background(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := InitContext(tt.args.ctx)
			fields := GetCtxFields(ctx)
			if len(fields) != 2 {
				t.Errorf("The fields length must be 2")
			}
			if fields[0] != LinkIDKey {
				t.Errorf("fields must be included: %s", LinkIDKey)
			}
		})
	}
}

func TestSetKeysAndValues(t *testing.T) {
	type tempStruct struct {
		a int
		b string
	}
	tempStruct1 := &tempStruct{111, "aaa"}
	type args struct {
		ctx           context.Context
		keysAndValues []interface{}
	}
	type want struct {
		index     int
		wantValue interface{}
	}
	tests := []struct {
		name string
		args args
		want []want
	}{
		{
			name: "case1",
			args: args{
				ctx:           InitContext(context.Background()),
				keysAndValues: []interface{}{"k1", "v1", "k2", "v2"},
			},
			want: []want{
				{
					index: 2, wantValue: "k1",
				},
				{
					index: 3, wantValue: "v1",
				},
				{
					index: 4, wantValue: "k2",
				},
				{
					index: 5, wantValue: "v2",
				},
			},
		},
		{
			name: "case2",
			args: args{
				ctx:           InitContext(context.Background()),
				keysAndValues: []interface{}{"k1", tempStruct{111, "aaa"}},
			},
			want: []want{
				{
					index: 2, wantValue: "k1",
				},
				{
					index: 3, wantValue: tempStruct{111, "aaa"},
				},
			},
		},
		{
			name: "case3",
			args: args{
				ctx:           InitContext(context.Background()),
				keysAndValues: []interface{}{"k1", tempStruct1},
			},
			want: []want{
				{
					index: 2, wantValue: "k1",
				},
				{
					index: 3, wantValue: tempStruct1,
				},
			},
		},
		{
			name: "case4",
			args: args{
				ctx:           context.Background(),
				keysAndValues: []interface{}{"k1", tempStruct1},
			},
			want: []want{
				{
					index: 0, wantValue: "k1",
				},
				{
					index: 1, wantValue: tempStruct1,
				},
			},
		},
		{
			name: "case4",
			args: args{
				ctx:           InitContext(context.Background()),
				keysAndValues: []interface{}{},
			},
			want: []want{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := SetKeysAndValues(tt.args.ctx, tt.args.keysAndValues...)
			fields := GetCtxFields(ctx)
			for _, w := range tt.want {
				if fields[w.index] != w.wantValue {
					t.Errorf("set KeysAndValues error, index(%d) should be %v, got: %v", w.index, w.wantValue, fields[w.index])
				} else {
					t.Logf("set KeysAndValues successful, index(%d) should be %v, got: %v", w.index, w.wantValue, fields[w.index])
				}
			}
		})
	}
}

// 自定义的测试日志记录器，实现 io.Writer 接口
type TestLogger struct {
	RegularxEpression string
	t                 *testing.T
}

func (tl *TestLogger) Write(p []byte) (n int, err error) {
	str := string(p)

	regex, err := regexp.Compile(tl.RegularxEpression)
	if err != nil {
		fmt.Println("Error compiling regex:", err)
		return
	}
	isMatch := regex.MatchString(str)
	if !isMatch {
		tl.t.Errorf("match the log content error, regex is %s, got: %s", tl.RegularxEpression, str)
	}

	return len(p), nil
}

func TestErrorS(t *testing.T) {
	type args struct {
		ctx           context.Context
		err           error
		msg           string
		keysAndValues []interface{}
	}
	tests := []struct {
		name       string
		args       args
		wantLogStr string
	}{
		{
			name: "case1",
			args: args{
				ctx:           SetKeysAndValues(context.Background(), "baseK1", "baseV1"),
				err:           fmt.Errorf("err1"),
				msg:           "errMsg1",
				keysAndValues: []interface{}{"k1", "v1", "k2", "v2"},
			},
			wantLogStr: "E\\d{4} \\d{2}:\\d{2}:\\d{2}\\.\\d{6}   %d logger_test.go:%d] \"errMsg1\" err=\"err1\" baseK1=\"baseV1\" k1=\"v1\" k2=\"v2\"\n",
		},
		{
			name: "case2",
			args: args{
				ctx:           context.Background(),
				err:           fmt.Errorf("err1"),
				msg:           "errMsg1",
				keysAndValues: []interface{}{},
			},
			wantLogStr: "E\\d{4} \\d{2}:\\d{2}:\\d{2}\\.\\d{6}   %d logger_test.go:%d] \"errMsg1\" err=\"err1\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, line, ok := runtime.Caller(0)
			if !ok {
				panic("Unable to retrieve caller information")
			}
			pid := os.Getpid()
			klog.LogToStderr(false)
			klog.SetOutput(&TestLogger{
				RegularxEpression: fmt.Sprintf(tt.wantLogStr, pid, line+10),
				t:                 t,
			})
			ErrorS(tt.args.ctx, tt.args.err, tt.args.msg, tt.args.keysAndValues...)
		})
	}
}

func TestInfoS(t *testing.T) {
	type args struct {
		ctx           context.Context
		msg           string
		keysAndValues []interface{}
	}
	tests := []struct {
		name       string
		args       args
		wantLogStr string
	}{
		{
			name: "case1",
			args: args{
				ctx:           SetKeysAndValues(context.Background(), "baseK1", "baseV1"),
				msg:           "InfoMsg1",
				keysAndValues: []interface{}{"k1", "v1", "k2", "v2"},
			},
			wantLogStr: "I\\d{4} \\d{2}:\\d{2}:\\d{2}\\.\\d{6}   %d logger_test.go:%d] \"InfoMsg1\" baseK1=\"baseV1\" k1=\"v1\" k2=\"v2\"\n",
		},
		{
			name: "case2",
			args: args{
				ctx:           context.Background(),
				msg:           "InfoMsg1",
				keysAndValues: []interface{}{},
			},
			wantLogStr: "I\\d{4} \\d{2}:\\d{2}:\\d{2}\\.\\d{6}   %d logger_test.go:%d] \"InfoMsg1\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, line, ok := runtime.Caller(0)
			if !ok {
				panic("Unable to retrieve caller information")
			}
			pid := os.Getpid()
			klog.LogToStderr(false)
			klog.SetOutput(&TestLogger{
				RegularxEpression: fmt.Sprintf(tt.wantLogStr, pid, line+10),
				t:                 t,
			})
			InfoS(tt.args.ctx, tt.args.msg, tt.args.keysAndValues...)
		})
	}
}

func TestVerbose_InfoS(t *testing.T) {
	logLevel := klog.Level(1)
	klog.InitFlags(nil)
	flag.Set("v", logLevel.String())
	flag.Parse()

	type fields struct {
		Verbose klog.Verbose
	}
	type args struct {
		ctx           context.Context
		msg           string
		keysAndValues []interface{}
	}
	tests := []struct {
		name       string
		fields     fields
		args       args
		wantLogStr string
	}{
		{
			name: "case1",
			args: args{
				ctx:           SetKeysAndValues(context.Background(), "baseK1", "baseV1"),
				msg:           "InfoMsg1",
				keysAndValues: []interface{}{"k1", "v1", "k2", "v2"},
			},
			wantLogStr: "I\\d{4} \\d{2}:\\d{2}:\\d{2}\\.\\d{6}   %d logger_test.go:%d] \"InfoMsg1\" baseK1=\"baseV1\" k1=\"v1\" k2=\"v2\"\n",
		},
		{
			name: "case2",
			args: args{
				ctx:           context.Background(),
				msg:           "InfoMsg1",
				keysAndValues: []interface{}{},
			},
			wantLogStr: "I\\d{4} \\d{2}:\\d{2}:\\d{2}\\.\\d{6}   %d logger_test.go:%d] \"InfoMsg1\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, line, ok := runtime.Caller(0)
			if !ok {
				panic("Unable to retrieve caller information")
			}
			pid := os.Getpid()
			klog.LogToStderr(false)
			klog.SetOutput(&TestLogger{
				RegularxEpression: fmt.Sprintf(tt.wantLogStr, pid, line+10),
				t:                 t,
			})
			V(logLevel).InfoS(tt.args.ctx, tt.args.msg, tt.args.keysAndValues...)
			klog.Flush()
		})
	}
}
