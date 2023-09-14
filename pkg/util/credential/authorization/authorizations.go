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

package authorization

import (
	"context"
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
)

type PermissionType string

type AccessControlType string

type AuthRule map[string][]PermissionType

const (
	// PermissionTypeHttpEndpoint represents all http resources
	PermissionTypeHttpEndpoint = "http_endpoint"
	// PermissionTypeEvictionPlugin represents the permission to register eviction plugin.
	PermissionTypeEvictionPlugin = "eviction_plugin"
	// PermissionTypeAll represents all permissions.
	PermissionTypeAll = "*"
)

const (
	AccessControlTypeInsecure     = "insecure"
	AccessControlTypeStatic       = "static"
	AccessControlTypeSecretBacked = "secret_backed"
)

const (
	ErrorMsgInvalidSubject = "there is no record associated to subject %v"
	ErrorMsgNoPermission   = "subject %v has no permission to resource %v"
)

// AccessControl verifies whether the subject the AuthInfo holds has the permission on the target resource.
type AccessControl interface {
	// Verify verifies whether the subject passed in has the permission on the target resource.
	Verify(authInfo credential.AuthInfo, targetResource PermissionType) error
	// Run starts the AccessControl component
	Run(ctx context.Context)
}

func verify(authInfo credential.AuthInfo, targetResource PermissionType, rules AuthRule) error {
	resources, ok := rules[authInfo.SubjectName()]
	if !ok {
		return fmt.Errorf(ErrorMsgInvalidSubject, authInfo.SubjectName())
	}

	for _, resource := range resources {
		if resource == targetResource || resource == PermissionTypeAll {
			return nil
		}
	}

	return fmt.Errorf(ErrorMsgNoPermission, authInfo.SubjectName(), targetResource)
}

type NewAccessControlFunc func(authConfig *generic.AuthConfiguration, clientSet *client.GenericClientSet) (AccessControl, error)

var accessControlInitializer = make(map[AccessControlType]NewAccessControlFunc)

func RegisterAccessControlInitializer(authType AccessControlType, initializer NewAccessControlFunc) {
	accessControlInitializer[authType] = initializer
}

func GetAccessControlInitializer() map[AccessControlType]NewAccessControlFunc {
	return accessControlInitializer
}

func init() {
	RegisterAccessControlInitializer(AccessControlTypeInsecure, NewInsecureAccessControl)
	RegisterAccessControlInitializer(AccessControlTypeSecretBacked, NewSecretBackedAccessControl)
	RegisterAccessControlInitializer(AccessControlTypeStatic, NewStaticAccessControl)
}

func GetAccessControl(genericConf *generic.GenericConfiguration, clientSet *client.GenericClientSet) (AccessControl, error) {
	accessControlInitializer, ok := GetAccessControlInitializer()[AccessControlType(genericConf.AuthConfiguration.AccessControlType)]
	if ok {
		ac, err := accessControlInitializer(genericConf.AuthConfiguration, clientSet)
		if err != nil {
			return nil, fmt.Errorf("initialize access control failed,type: %v, err: %v", genericConf.AccessControlType, err)
		}

		return ac, nil
	} else {
		return nil, fmt.Errorf("unsupported access control type: %v", genericConf.AuthConfiguration.AccessControlType)
	}
}

func DefaultAccessControl() AccessControl {
	return &insecureAccessControl{}
}
