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

package service

import (
	"context"
	"strings"

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

// ProductRepoMappingService resolves a case product name to a GitHub repo.
type ProductRepoMappingService struct {
	repo *repository.ProductRepoMappingRepository
}

// NewProductRepoMappingService returns a service bound to repo.
func NewProductRepoMappingService(repo *repository.ProductRepoMappingRepository) *ProductRepoMappingService {
	return &ProductRepoMappingService{repo: repo}
}

// Find returns the mapping for name.
// An exact product name wins, then an exact abbreviation, then the longest
// product name that is a prefix of name followed by a space (a version).
func (s *ProductRepoMappingService) Find(ctx context.Context, name string) (domain.ProductRepoMapping, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.ProductRepoMapping{}, &apierror.ValidationError{Msg: "Product name is required."}
	}
	rows, err := s.repo.ListActive(ctx)
	if err != nil {
		return domain.ProductRepoMapping{}, err
	}
	found, ok := MatchProductRepo(rows, name)
	if !ok {
		return domain.ProductRepoMapping{}, &apierror.NotFoundError{Msg: "No GitHub repository is mapped for this product."}
	}
	return found, nil
}

// MatchProductRepo applies the lookup rules to an already-loaded catalogue.
func MatchProductRepo(rows []domain.ProductRepoMapping, name string) (domain.ProductRepoMapping, bool) {
	query := strings.TrimSpace(name)
	if query == "" {
		return domain.ProductRepoMapping{}, false
	}
	folded := strings.ToLower(query)

	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.ProductName), query) {
			return row, true
		}
	}
	for _, row := range rows {
		if row.Abbreviation != nil && strings.EqualFold(strings.TrimSpace(*row.Abbreviation), query) {
			return row, true
		}
	}

	var best domain.ProductRepoMapping
	bestLen := -1
	for _, row := range rows {
		product := strings.TrimSpace(row.ProductName)
		prefix := strings.ToLower(product) + " "
		if strings.HasPrefix(folded, prefix) && len(product) > bestLen {
			best = row
			bestLen = len(product)
		}
	}
	if bestLen < 0 {
		return domain.ProductRepoMapping{}, false
	}
	return best, true
}
