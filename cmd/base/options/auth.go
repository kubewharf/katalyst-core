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

	// AccessControl type
	AccessControlType string
}

func NewAuthOptions() *AuthOptions {
	return &AuthOptions{
		AuthType:          credential.AuthTypeInsecure,
		AccessControlType: authorization.AccessControlTypeInsecure,
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *AuthOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.AuthType, "auth-type", o.AuthType, "which auth type for common http endpoint,"+
		"e.g. BasicAuth")
	fs.StringVar(&o.AccessControlType, "access-control-type", o.AccessControlType, "access control type")
}

func (o *AuthOptions) ApplyTo(c *generic.AuthConfiguration) error {
	c.AuthType = o.AuthType
	c.AccessControlType = o.AccessControlType

	return nil
}
