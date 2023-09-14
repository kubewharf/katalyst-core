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

package options

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
	"github.com/kubewharf/katalyst-core/pkg/util/credential/authorization"
)

type AuthOptions struct {
	// Authentication type
	AuthType string

	// Configurations about BasicAuth,it only takes effect when AuthType is credential.AuthTypeBasicAuth
	BasicAuthSecretNameSpace string
	BasicAuthSecretName      string

	// AccessControl type
	AccessControlType string

	// Configurations about SecretBacked AccessControl, it only takes effects when AccessControlType
	// is authorization.AccessControlTypeSecretBacked
	AccessControlSecretNameSpace string
	AccessControlSecretName      string
}

func NewAuthOptions() *AuthOptions {
	return &AuthOptions{
		AuthType: credential.AuthTypeInsecure,

		BasicAuthSecretName:      "katalyst-acconuts",
		BasicAuthSecretNameSpace: "default",

		AccessControlType: authorization.AccessControlTypeInsecure,

		AccessControlSecretName:      "katalyst-access-rules",
		AccessControlSecretNameSpace: "default",
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *AuthOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.AuthType, "auth-type", o.AuthType, "which auth type for common http endpoint,"+
		"e.g. BasicAuth")

	fs.StringVar(&o.BasicAuthSecretName, "basic-auth-secret-name", o.BasicAuthSecretName, "the secret name which stores"+
		" the username and password pairs")
	fs.StringVar(&o.BasicAuthSecretNameSpace, "basic-auth-secret-namespace", o.BasicAuthSecretNameSpace, "the namespace "+
		"where the secret which stores the username and password pairs is in")

	fs.StringVar(&o.AccessControlType, "access-control-type", o.AccessControlType, "access control type")

	fs.StringVar(&o.AccessControlSecretName, "access-control-secret-name", o.AccessControlSecretName, "the secret "+
		"name which stores the access control rules")
	fs.StringVar(&o.AccessControlSecretNameSpace, "access-control-secret-namespace", o.AccessControlSecretNameSpace, "the secret "+
		"name which stores the access control rules")
}

func (o *AuthOptions) ApplyTo(c *generic.AuthConfiguration) error {
	c.AuthType = o.AuthType

	c.BasicAuthSecretName = o.BasicAuthSecretName
	c.BasicAuthSecretNameSpace = o.BasicAuthSecretNameSpace

	c.AccessControlType = o.AccessControlType

	c.AccessControlSecretName = o.AccessControlSecretName
	c.AccessControlSecretNameSpace = o.AccessControlSecretNameSpace

	return nil
}
