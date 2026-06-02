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

package validator

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/validator"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

type nicAnnotationValidator struct {
	conf *config.Configuration
}

func NewNICAnnotationValidator(conf *config.Configuration) validator.AnnotationValidator {
	return &nicAnnotationValidator{
		conf: conf,
	}
}

func (n *nicAnnotationValidator) ValidatePodAnnotation(ctx context.Context, podAnnotation map[string]string) (bool, error) {
	annoList := map[string]string{
		n.conf.NetworkQRMPluginConfig.NetBandwidthResourceAllocationAnnotationKey: "",
	}

	return validator.ValidatePodAnnotations(podAnnotation, annoList)
}
