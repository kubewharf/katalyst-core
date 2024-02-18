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

import "fmt"

const (
	Validated Phase = "Validated"
)

const (
	WorkloadNameIsEmpty              Code = "WorkloadNameIsEmpty"
	WorkloadsUnsupported             Code = "WorkloadsUnsupported"
	AlgorithmUnsupported             Code = "AlgorithmUnsupported"
	WorkloadMatchError               Code = "WorkloadMatchError"
	WorkloadNotFound                 Code = "WorkloadNotFound"
	ContainerPoliciesNotFound        Code = "ContainerPoliciesNotFound"
	ContainersNotFound               Code = "ContainersNotFound"
	ContainersMatchErrorCode         Code = "ContainersMatchErrorCode"
	ContainerDuplicate               Code = "ContainerDuplicate"
	ContainerNameEmpty               Code = "ContainerNameEmpty"
	ControlledResourcesPoliciesEmpty Code = "ControlledResourcesPoliciesEmpty"
	ResourceBuffersUnsupported       Code = "ResourceBuffersUnsupported"
	ResourceNameUnsupported          Code = "ResourceNameUnsupported"
	ControlledValuesUnsupported      Code = "ControlledValuesUnsupported"
)

const (
	WorkloadNameIsEmptyMessage              = "spec.targetRef.name cannot be empty"
	WorkloadsUnsupportedMessage             = "spec.targetRef.kind %s is currently unsupported, only support in %s"
	AlgorithmUnsupportedMessage             = "spec.resourcePolicy.algorithmPolicy.algorithm %s is currently unsupported, only support in %s"
	WorkloadNotFoundMessage                 = "workload not found"
	WorkloadMatchedErrorMessage             = "workload matched err"
	ContainerPoliciesNotFoundMessage        = "spec.containerPolicies cannot be empty"
	ContainersMatchedErrorMessage           = "containers matched err"
	ContainerDuplicateMessage               = "container name %s is duplicate"
	ContainerNameEmptyMessage               = "empty container name"
	ControlledResourcesPoliciesEmptyMessage = "container(%s) Resources Policies is empty"
	ContainerNotFoundMessage                = "container %s is not found"
	ResourceNameUnsupportedMessage          = "unsupported ResourceName, current supported values: %s"
	ControlledValuesUnsupportedMessage      = "unsupported ControlledValues, current supported values: %s"
	ResourceBuffersUnsupportedMessage       = "resource buffers should be in the range from 0 to 100"
)

func WorkloadNameIsEmptyError() *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    WorkloadNameIsEmpty,
		Message: WorkloadNameIsEmptyMessage,
	}
}

func WorkloadsUnsupportedError(kind string, supportedKinds []string) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    WorkloadsUnsupported,
		Message: fmt.Sprintf(WorkloadsUnsupportedMessage, kind, supportedKinds),
	}
}

func AlgorithmUnsupportedError(algorithm string, supportedAlgorithm []string) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    AlgorithmUnsupported,
		Message: fmt.Sprintf(AlgorithmUnsupportedMessage, algorithm, supportedAlgorithm),
	}
}

func ResourceNameUnsupportedError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    ResourceNameUnsupported,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func ControlledValuesUnsupportedError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    ControlledValuesUnsupported,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func ResourceBuffersUnsupportedError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    ResourceBuffersUnsupported,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func WorkloadMatchedError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    WorkloadMatchError,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func WorkloadNotFoundError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    WorkloadNotFound,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func ContainerPoliciesNotFoundError() *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    ContainerPoliciesNotFound,
		Message: ContainerPoliciesNotFoundMessage,
	}
}

func ContainersMatchedError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    ContainersMatchErrorCode,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func ContainerNameEmptyError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    ContainerNameEmpty,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func ControlledResourcesPoliciesEmptyError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    ControlledResourcesPoliciesEmpty,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func ContainersNotFoundError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    ContainersNotFound,
		Message: fmt.Sprintf(msg, arg...),
	}
}

func ContainerDuplicateError(msg string, arg ...any) *CustomError {
	return &CustomError{
		Phase:   Validated,
		Code:    ContainerDuplicate,
		Message: fmt.Sprintf(msg, arg...),
	}
}
