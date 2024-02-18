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

package error

import (
	"fmt"

	"k8s.io/apimachinery/pkg/types"
)

const (
	ProcessRegister Phase = "ProcessRegister"
	ProcessCancel   Phase = "ProcessCancel"
)

const (
	DataProcessRegisterFailed   Code = "DataProcessRegisterFailed"
	ValidateProcessConfigFailed Code = "ValidateProcessConfigFailed"
	RegisterTaskPanic           Code = "RegisterTaskPanic"
	CancelTaskPanic             Code = "CancelTaskPanic"
	NewProcessTaskFailed        Code = "NewProcessTaskFailed"
	NotFoundTasks               Code = "NotFoundTask"
)

const (
	RegisterProcessTaskPanicMessage         = "register data process task panic"
	CancelProcessTaskPanicMessage           = "cancel data process task panic"
	ProcessTaskValidateErrorMessageTemplate = "register data process task failed, get error when validate, err: %s"
	NewProcessTaskErrorMessageTemplate      = "register data process task failed, fail to get new task, err: %s"
	TasksNotFoundErrorMessage               = "process tasks not found "
)

func DataProcessRegisteredFailedError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   ProcessRegister,
		Code:    DataProcessRegisterFailed,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func RegisterProcessTaskValidateError(err error) *CustomError {
	return &CustomError{
		Phase:   ProcessRegister,
		Code:    ValidateProcessConfigFailed,
		Message: fmt.Sprintf(ProcessTaskValidateErrorMessageTemplate, err),
	}
}

func RegisterProcessTaskPanic() *CustomError {
	return &CustomError{
		Phase:   ProcessRegister,
		Code:    RegisterTaskPanic,
		Message: RegisterProcessTaskPanicMessage,
	}
}

func NewProcessTaskError(err error) *CustomError {
	return &CustomError{
		Phase:   ProcessRegister,
		Code:    NewProcessTaskFailed,
		Message: fmt.Sprintf(NewProcessTaskErrorMessageTemplate, err),
	}
}

func CancelProcessTaskPanic() *CustomError {
	return &CustomError{
		Phase:   ProcessCancel,
		Code:    CancelTaskPanic,
		Message: CancelProcessTaskPanicMessage,
	}
}

func NotFoundTasksError(namespacedName types.NamespacedName) *CustomError {
	return &CustomError{
		Phase:   ProcessCancel,
		Code:    NotFoundTasks,
		Message: fmt.Sprintf(TasksNotFoundErrorMessage+"for ResourceRecommend(%s)", namespacedName),
	}
}
