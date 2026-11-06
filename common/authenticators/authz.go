/*
 * Copyright (c) 2025, WSO2 LLC. (https://www.wso2.com).
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
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */
package authenticators

import (
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	commonerrors "github.com/wso2/api-platform/common/errors"
	"github.com/wso2/api-platform/common/models"
	"github.com/wso2/api-platform/httpkit/httputil"
)

// AuthorizationMiddleware enforces resource->roles mapping stored in config.ResourceRoles.
// Route keys use the net/http ServeMux pattern format: "METHOD /path/{param}".
func AuthorizationMiddleware(config models.AuthConfig, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resourcePattern := r.Pattern
			if required, found := config.ResourceScopes[resourcePattern]; found {
				// No-auth mode and skip paths must not bypass a scoped route.
				if GetAuthzSkip(r) {
					httputil.WriteError(w, http.StatusForbidden, "forbidden", commonerrors.ErrForbidden.Error())
					return
				}
				ac, ok := GetAuthContext(r)
				if !ok || !ac.Authenticated {
					httputil.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
					return
				}
				granted := make(map[string]bool)
				for _, role := range ac.Roles {
					granted[role] = true // an IDP roles claim may contain scopes directly
					if strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
						for _, scope := range config.BasicRoleScopes[role] {
							granted[scope] = true
						}
					}
				}
				claims := reflect.ValueOf(ac.Claims)
				if claims.IsValid() && claims.Kind() == reflect.Map {
					rawValue := claims.MapIndex(reflect.ValueOf("scope"))
					if rawValue.IsValid() {
						raw := rawValue.Interface()
						switch scopes := raw.(type) {
						case string:
							for _, scope := range strings.Fields(scopes) {
								granted[scope] = true
							}
						case []any:
							for _, entry := range scopes {
								if scope, ok := entry.(string); ok {
									granted[scope] = true
								}
							}
						}
					}
				}
				for _, scope := range required {
					if granted[scope] {
						next.ServeHTTP(w, r)
						return
					}
				}
				httputil.WriteError(w, http.StatusForbidden, "forbidden", commonerrors.ErrForbidden.Error())
				return
			}
			// Skip authorization if authentication was skipped
			if GetAuthzSkip(r) {
				next.ServeHTTP(w, r)
				return
			}

			resourceRoles := config.ResourceRoles
			logger.Debug("Resource roles", slog.Any("resourceRoles", resourceRoles))
			if len(resourceRoles) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			// Retrieve user roles stored by AuthMiddleware
			var userRoles []string
			if ac, ok := GetAuthContext(r); ok {
				userRoles = ac.Roles
			}
			logger.Debug("User roles", slog.Any("userRoles", userRoles))

			// r.Pattern is set by net/http ServeMux (Go 1.22+) to the matched
			// route pattern including method, e.g. "GET /management/v1/apis/{id}".
			// An empty pattern means no route matched — skip authz and let the
			// mux return 404.
			resourcePattern = r.Pattern
			if resourcePattern == "" {
				next.ServeHTTP(w, r)
				return
			}

			logger.Debug("resource pattern", slog.String("pattern", resourcePattern))
			allowed, found := resourceRoles[resourcePattern]
			if !found {
				httputil.WriteError(w, http.StatusForbidden, "forbidden", commonerrors.ErrForbidden.Error())
				return
			}

			allowedSet := make(map[string]struct{}, len(allowed))
			for _, role := range allowed {
				allowedSet[role] = struct{}{}
			}
			for _, ur := range userRoles {
				if _, ok := allowedSet[ur]; ok {
					next.ServeHTTP(w, r)
					return
				}
			}

			httputil.WriteError(w, http.StatusForbidden, "forbidden", commonerrors.ErrForbidden.Error())
		})
	}
}
