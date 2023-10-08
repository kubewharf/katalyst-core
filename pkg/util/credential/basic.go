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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	secretSyncInterval = 30 * time.Second
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

func NewBasicAuthCredential(_ *generic.AuthConfiguration, dynamicConfiguration *dynamic.DynamicAgentConfiguration) (Credential, error) {
	return &basicAuthCredential{
		authPairs:     map[string]string{},
		dynamicConfig: dynamicConfiguration,
	}, nil
}

type basicAuthCredential struct {
	mutex         sync.RWMutex
	dynamicConfig *dynamic.DynamicAgentConfiguration
	authPairs     map[string]string
}

func (b *basicAuthCredential) Run(ctx context.Context) {
	go wait.Until(b.updateAuthPairFromDynamicConf, secretSyncInterval, ctx.Done())
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
	b.mutex.RLock()
	defer b.mutex.RUnlock()

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

func (b *basicAuthCredential) updateAuthPairFromDynamicConf() {
	dynamicConfiguration := b.dynamicConfig.GetDynamicConfiguration()
	newAuthPairs := make(map[string]string)
	for _, pair := range dynamicConfiguration.UserPasswordPairs {
		p, err := base64.StdEncoding.DecodeString(pair.Password)
		if err != nil {
			general.Warningf("fail to decode password, err: %v", err)
			continue
		}
		newAuthPairs[pair.Username] = string(p)
	}
	b.mutex.Lock()
	b.authPairs = newAuthPairs
	b.mutex.Unlock()
}
