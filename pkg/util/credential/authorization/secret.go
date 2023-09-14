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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	resourceSeparator = ","

	secretSyncInterval = 5 * time.Minute
)

func NewSecretBackedAccessControl(conf *generic.AuthConfiguration, client *client.GenericClientSet) (AccessControl, error) {
	return &secretBackedAccessControl{
		namespace:            conf.AccessControlSecretNameSpace,
		name:                 conf.AccessControlSecretName,
		kubeClient:           client.KubeClient,
		subjectToPermissions: map[string][]PermissionType{},
	}, nil
}

type secretBackedAccessControl struct {
	namespace            string
	name                 string
	kubeClient           kubernetes.Interface
	subjectToPermissions AuthRule
}

func (s *secretBackedAccessControl) Run(ctx context.Context) {
	go wait.Until(s.updateAuthInfoFromSecret, secretSyncInterval, ctx.Done())
}

func (s *secretBackedAccessControl) Verify(authInfo credential.AuthInfo, targetResource PermissionType) error {
	return verify(authInfo, targetResource, s.subjectToPermissions)
}

func (s *secretBackedAccessControl) updateAuthInfoFromSecret() {
	secret, err := s.kubeClient.CoreV1().Secrets(s.namespace).Get(context.TODO(), s.name, metav1.GetOptions{})
	if err == nil {
		newRule := make(AuthRule)
		for subject, permissionStr := range secret.StringData {
			permissions := strings.Split(permissionStr, resourceSeparator)
			rule := make([]PermissionType, 0, len(permissions))
			for i := range permissions {
				rule = append(rule, PermissionType(permissions[i]))
			}
			newRule[subject] = rule
		}
		s.subjectToPermissions = newRule
	} else {
		general.Errorf("get access control secret failed:%v", err)
	}
}
