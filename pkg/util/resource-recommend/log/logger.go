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

	"github.com/google/uuid"
	"k8s.io/klog/v2"
)

// This log is only used in resource-recommend.
// TODO: How to do the common log needs to be discussed again

var fieldsKey = &[1]int{1}

const LinkIDKey = "linkId"

// InitContext set context info such as accountId for logger
func InitContext(ctx context.Context) context.Context {
	fields := []interface{}{
		LinkIDKey, uuid.New(),
	}
	return context.WithValue(ctx, fieldsKey, fields)
}

// SetKeysAndValues set KeysAndValues into ctx
func SetKeysAndValues(ctx context.Context, keysAndValues ...interface{}) context.Context {
	ctxFields := ctx.Value(fieldsKey)
	if ctxFields == nil {
		return context.WithValue(ctx, fieldsKey, keysAndValues)
	}
	if fields, ok := ctxFields.([]interface{}); !ok {
		return context.WithValue(ctx, fieldsKey, keysAndValues)
	} else {
		fields = append(fields, keysAndValues...)
		return context.WithValue(ctx, fieldsKey, fields)
	}
}

func GetCtxFields(ctx context.Context) []interface{} {
	ctxFields := ctx.Value(fieldsKey)
	if ctxFields == nil {
		return []interface{}{}
	}
	if fields, ok := ctxFields.([]interface{}); ok {
		return fields
	}
	return []interface{}{}
}

// ErrorS print error log
func ErrorS(ctx context.Context, err error, msg string, keysAndValues ...interface{}) {
	fields := GetCtxFields(ctx)
	fields = append(fields, keysAndValues...)
	if len(fields) == 0 {
		klog.ErrorSDepth(1, err, msg)
	} else {
		klog.ErrorSDepth(1, err, msg, fields...)
	}
}

// InfoS print info log
func InfoS(ctx context.Context, msg string, keysAndValues ...interface{}) {
	fields := GetCtxFields(ctx)
	fields = append(fields, keysAndValues...)
	if len(fields) == 0 {
		klog.InfoSDepth(1, msg)
	} else {
		klog.InfoSDepth(1, msg, fields...)
	}
}

type Verbose struct {
	klog.Verbose
}

func V(level klog.Level) Verbose {
	return Verbose{Verbose: klog.V(level)}
}

// InfoS print info log
func (v Verbose) InfoS(ctx context.Context, msg string, keysAndValues ...interface{}) {
	fields := GetCtxFields(ctx)
	fields = append(fields, keysAndValues...)
	if len(fields) == 0 {
		v.Verbose.InfoSDepth(1, msg)
	} else {
		v.Verbose.InfoSDepth(1, msg, fields...)
	}
}
