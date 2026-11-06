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
	"testing"

	"github.com/stretchr/testify/require"
	policy "github.com/wso2/api-platform/sdk/core/policy/v1alpha2"
)

func TestCELRequestAuthUsesValidatedTypedClaims(t *testing.T) {
	evaluator, err := NewCELEvaluator()
	require.NoError(t, err)
	ctx := &policy.RequestHeaderContext{
		SharedContext: &policy.SharedContext{AuthContext: &policy.AuthContext{
			Previous: &policy.AuthContext{Authenticated: true, AuthType: "jwt", Subject: "alice",
				TypedProperties: map[string]any{"groups": []string{"R&D", "AI"}}},
		}},
		Headers: policy.NewHeaders(nil),
	}
	match, err := evaluator.EvaluateRequestHeaderCondition(
		`request.Auth.Subject == "alice" && "R&D" in request.Auth.Claims["groups"]`, ctx)
	require.NoError(t, err)
	require.True(t, match)

	ctx.AuthContext = &policy.AuthContext{Authenticated: false, TypedProperties: map[string]any{"groups": []string{"R&D"}}}
	match, err = evaluator.EvaluateRequestHeaderCondition(`request.Auth.Subject == "alice"`, ctx)
	require.NoError(t, err)
	require.False(t, match, "unvalidated claims must not enter CEL")
}
