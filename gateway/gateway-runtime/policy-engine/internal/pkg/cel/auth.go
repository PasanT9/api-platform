/*
 * Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

package cel

import (
	"sort"

	policy "github.com/wso2/api-platform/sdk/core/policy/v1alpha2"
)

// AuthToCEL exposes only claims from a successfully authenticated context.
// TypedProperties preserves claim arrays and objects for CEL membership tests.
func AuthToCEL(context *policy.AuthContext) map[string]any {
	claims := map[string]any{}
	result := map[string]any{"Claims": claims, "Subject": "", "Type": ""}
	for context != nil && !context.Authenticated {
		context = context.Previous
	}
	if context == nil {
		return result
	}
	for key, value := range context.TypedProperties {
		claims[key] = value
	}
	if context.Subject != "" {
		claims["sub"] = context.Subject
	}
	if context.Issuer != "" {
		claims["iss"] = context.Issuer
	}
	if len(context.Audience) > 0 {
		claims["aud"] = context.Audience
	}
	if len(context.Scopes) > 0 {
		scopes := make([]string, 0, len(context.Scopes))
		for scope := range context.Scopes {
			scopes = append(scopes, scope)
		}
		sort.Strings(scopes)
		claims["scope"] = scopes
	}
	result["Subject"] = context.Subject
	result["Type"] = context.AuthType
	return result
}

func withAuthVars(activation map[string]interface{}, shared *policy.SharedContext) map[string]interface{} {
	var auth *policy.AuthContext
	if shared != nil {
		auth = shared.AuthContext
	}
	value := AuthToCEL(auth)
	request := activation["request"].(map[string]interface{})
	request["Auth"] = value
	activation["request.Auth"] = value
	activation["request.Auth.Claims"] = value["Claims"]
	activation["request.Auth.Subject"] = value["Subject"]
	activation["request.Auth.Type"] = value["Type"]
	return activation
}
