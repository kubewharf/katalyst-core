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

package credential

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	secretSyncInterval = 10 * time.Minute
)

type BasicAuthInfo struct {
	Username string
	Password string
}

func (b BasicAuthInfo) AuthType() AuthType {
	return AuthTypeBasicAuth
}

func (b BasicAuthInfo) SubjectName() string {
	return b.Username
}

func NewBasicAuthCredential(authConfig *generic.AuthConfiguration, clientSet *client.GenericClientSet) (Credential, error) {
	return &basicAuthCredential{
		kubeClient: clientSet.KubeClient,
		namespace:  authConfig.BasicAuthSecretNameSpace,
		name:       authConfig.BasicAuthSecretName,
		authPairs:  map[string]string{},
	}, nil
}

type basicAuthCredential struct {
	kubeClient kubernetes.Interface
	namespace  string
	name       string
	authPairs  map[string]string
}

func (b *basicAuthCredential) Run(ctx context.Context) {
	go wait.Until(b.updateAuthPairFromSecret, secretSyncInterval, ctx.Done())
}

func (b *basicAuthCredential) AuthType() AuthType {
	return AuthTypeBasicAuth
}

func (b *basicAuthCredential) Auth(r *http.Request) (AuthInfo, error) {
	username, password, ok := r.BasicAuth()

	if !ok {
		return nil, fmt.Errorf("invalid basic auth token:%v", r.Header.Get("Authorization"))
	}

	err := b.verifyAuthInfo(username, password)
	if err != nil {
		return nil, err
	}

	return BasicAuthInfo{Username: username, Password: password}, nil
}

func (b *basicAuthCredential) AuthToken(token string) (AuthInfo, error) {
	username, password, ok := parseBasicAuth(token)
	if !ok {
		return nil, fmt.Errorf("invalid basic auth token:%v", token)
	}

	err := b.verifyAuthInfo(username, password)
	if err != nil {
		return nil, err
	}

	return BasicAuthInfo{Username: username, Password: password}, nil
}

func (b *basicAuthCredential) verifyAuthInfo(username, password string) error {
	storedPassword, ok := b.authPairs[username]
	if !ok {
		return fmt.Errorf("user %v not found in store", username)
	}
	if storedPassword != password {
		return fmt.Errorf("password for user %v are wrong", username)
	}

	return nil
}

// parseBasicAuth parses an HTTP Basic Authentication string.
// "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==" returns ("Aladdin", "open sesame", true).
func parseBasicAuth(auth string) (username, password string, ok bool) {
	const prefix = "Basic "
	// Case-insensitive prefix match.
	if len(auth) < len(prefix) || !strings.EqualFold(strings.ToLower(auth[:len(prefix)]), strings.ToLower(prefix)) {
		return "", "", false
	}
	c, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return "", "", false
	}
	cs := string(c)
	username, password, ok = strings.Cut(cs, ":")
	if !ok {
		return "", "", false
	}
	return username, password, true
}

func (b *basicAuthCredential) updateAuthPairFromSecret() {
	secret, err := b.kubeClient.CoreV1().Secrets(b.namespace).Get(context.TODO(), b.name, metav1.GetOptions{})
	if err != nil {
		general.Errorf("get auth pair secret failed:%v", err)
	} else {
		newAuthPairs := make(map[string]string)
		for username, password := range secret.StringData {
			newAuthPairs[username] = password
		}
		b.authPairs = newAuthPairs
	}
}
