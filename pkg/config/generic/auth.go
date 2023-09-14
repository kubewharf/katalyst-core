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

package generic

// AuthConfiguration stores all configurations related to authentication and authorization
type AuthConfiguration struct {
	// Authentication type
	AuthType string

	// Configurations about BasicAuth
	BasicAuthSecretNameSpace string
	BasicAuthSecretName      string

	AccessControlType string

	// Configurations about SecretBacked AccessControl
	AccessControlSecretNameSpace string
	AccessControlSecretName      string
}

func NewAuthConfiguration() *AuthConfiguration {
	return &AuthConfiguration{}
}
