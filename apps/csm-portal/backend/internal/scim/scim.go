// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package scim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// org is fixed to "internal" — the CSM portal is exclusively for WSO2 employees.
	org = "internal"

	// orgExternal is the SCIM org that holds customer/partner contacts, used
	// to check whether an external contact's account exists and is locked.
	orgExternal = "external"

	domainDefault    = "DEFAULT"
	attrPhoneNumbers = "phoneNumbers"
	attrUserName     = "userName"
	attrSchema       = "urn:scim:wso2:schema"
	attrRoles        = "roles"

	mobilePhoneType = "mobile"

	accountStateLocked = "LOCKED"
)

// SearchUser fetches SCIM data for the given email and returns the extracted
// phone number, last password update time, and role assignment, mirroring
// searchUsers + processPhoneNumber + processLastPasswordUpdateTime in the
// Ballerina SCIM module (roles has no Ballerina-side precedent -- it backs the
// profile page's own role display, added later than that mirrored logic).
// Returns nil if no matching user is found in the SCIM service.
func (c *Client) SearchUser(ctx context.Context, email string) (*UserInfo, error) {
	startIndex := 1
	var found *scimUser

	for {
		reqBody, err := json.Marshal(scimSearchRequest{
			Domain:     domainDefault,
			Attributes: []string{attrPhoneNumbers, attrUserName, attrSchema, attrRoles},
			Filter:     fmt.Sprintf("userName eq %s", email),
			StartIndex: startIndex,
		})
		if err != nil {
			return nil, fmt.Errorf("scim: encode search request: %w", err)
		}

		raw, err := c.do(ctx, http.MethodPost, "/organizations/"+org+"/users/search", reqBody)
		if err != nil {
			return nil, err
		}

		var result scimSearchResponse
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("scim: decode search response: %w", err)
		}

		if len(result.Resources) > 0 && found == nil {
			u := result.Resources[0]
			found = &u
		}

		// Pagination mirrors the Ballerina while-loop condition:
		// moreUsersExist = (startIndex + itemsPerPage - 1) < totalResults
		if result.ItemsPerPage == 0 || (startIndex+result.ItemsPerPage-1) >= result.TotalResults {
			break
		}
		startIndex += result.ItemsPerPage
	}

	if found == nil {
		return nil, nil
	}

	return &UserInfo{
		PhoneNumber:            extractMobilePhone(*found),
		LastPasswordUpdateTime: extractLastPasswordUpdateTime(*found),
		Roles:                  []string(found.Roles),
	}, nil
}

// SearchExternalUser checks whether the given email exists in the SCIM
// "external" org and, if so, whether the account is locked, mirroring
// asgardeo-user-check's searchUser. A single itemsPerPage=1 lookup is enough
// to answer an existence check, so unlike SearchUser this does not paginate.
func (c *Client) SearchExternalUser(ctx context.Context, email string) (*ExternalUserInfo, error) {
	reqBody, err := json.Marshal(scimExternalSearchRequest{
		Attributes:   []string{attrUserName, attrSchema},
		Filter:       fmt.Sprintf("userName eq %q", email),
		ItemsPerPage: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("scim: encode external search request: %w", err)
	}

	raw, err := c.do(ctx, http.MethodPost, "/organizations/"+orgExternal+"/users/search", reqBody)
	if err != nil {
		return nil, err
	}

	var result scimSearchResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("scim: decode external search response: %w", err)
	}

	if result.TotalResults == 0 || len(result.Resources) == 0 {
		return &ExternalUserInfo{Exists: false}, nil
	}

	return &ExternalUserInfo{
		Exists: true,
		Locked: extractAccountLocked(result.Resources[0]),
	}, nil
}

// extractAccountLocked mirrors asgardeo-user-check's extractLocked: Asgardeo
// returns accountLocked as a quoted string ("true"/"false"), not a JSON
// boolean, so it must be parsed rather than decoded directly. Falls back to
// accountState ("LOCKED"/"UNLOCKED") when accountLocked is absent or
// unparseable, and returns nil if neither yields a determinate answer.
func extractAccountLocked(u scimUser) *bool {
	if u.SchemaScope == nil {
		return nil
	}
	if u.SchemaScope.AccountLocked != nil {
		if v, err := strconv.ParseBool(*u.SchemaScope.AccountLocked); err == nil {
			return &v
		}
	}
	if u.SchemaScope.AccountState != nil {
		v := strings.EqualFold(*u.SchemaScope.AccountState, accountStateLocked)
		return &v
	}
	return nil
}

// UpdateUserPhone updates the mobile phone number for the given user via a SCIM
// PATCH and returns the updated phone number extracted from the response,
// mirroring updateUser in the Ballerina SCIM module.
func (c *Client) UpdateUserPhone(ctx context.Context, userID, mobile string) (*string, error) {
	reqBody, err := json.Marshal(scimUpdateRequest{
		PhoneNumber: &scimPhonePayload{Mobile: mobile},
	})
	if err != nil {
		return nil, fmt.Errorf("scim: encode update request: %w", err)
	}

	path := "/organizations/internal/users/" + url.PathEscape(userID)
	raw, err := c.do(ctx, http.MethodPatch, path, reqBody)
	if err != nil {
		return nil, err
	}

	var updatedUser scimUser
	if err := json.Unmarshal(raw, &updatedUser); err != nil {
		return nil, fmt.Errorf("scim: decode update response: %w", err)
	}

	return extractMobilePhone(updatedUser), nil
}

// extractMobilePhone returns the first phone number of type "mobile", or nil.
// Mirrors processPhoneNumber in the Ballerina SCIM utils.
func extractMobilePhone(u scimUser) *string {
	for _, p := range u.PhoneNumbers {
		if p.Type == mobilePhoneType {
			v := p.Value
			return &v
		}
	}
	return nil
}

// extractLastPasswordUpdateTime returns the lastPasswordUpdateTime from the
// WSO2 SCIM extension schema, or nil. Mirrors processLastPasswordUpdateTime.
func extractLastPasswordUpdateTime(u scimUser) *string {
	if u.SchemaScope != nil {
		return u.SchemaScope.LastPasswordUpdateTime
	}
	return nil
}
