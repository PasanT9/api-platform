/*
 * Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/models"
)

// BudgetStorage is separate from Storage so existing test stores and unrelated
// controllers do not need to implement budget-specific operations.
type BudgetStorage interface {
	CreateBudget(*models.StoredBudget) error
	GetBudget(string) (*models.StoredBudget, error)
	ListBudgets() ([]*models.StoredBudget, error)
	UpdateBudget(*models.StoredBudget) error
	DeleteBudget(string) error
	// GetBudgetByUUID reads a budget by its UUID, which the control plane assigns.
	GetBudgetByUUID(string) (*models.StoredBudget, error)
	// UpsertControlPlaneBudget saves a budget received from the control plane
	// under the control plane's UUID and revision. It applies only a revision
	// newer than the stored one and reports whether it changed anything.
	UpsertControlPlaneBudget(*models.StoredBudget) (bool, error)
	// DeleteBudgetByUUID deletes a budget by UUID.
	DeleteBudgetByUUID(string) error
}

var budgetSelectColumns = `uuid, gateway_id, handle, name, version, revision, data_version, status, origin,
    resource_kind, resource_uuid, configuration, created_by, created_at, updated_by, updated_at`

func (s *sqlStore) CreateBudget(b *models.StoredBudget) error {
	configuration, err := json.Marshal(b.Document)
	if err != nil {
		return fmt.Errorf("encode budget: %w", err)
	}
	tx, err := s.db.BeginTx(s.ctx, nil)
	if err != nil {
		return fmt.Errorf("begin budget create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if b.ResourceKind != "" && b.Document.Spec.Resource != nil && b.Document.Spec.Resource.ID != "" {
		query := `SELECT uuid FROM artifacts WHERE gateway_id = ? AND kind = ? AND handle = ?`
		switch s.backendName {
		case "postgres":
			query += ` FOR UPDATE`
		case "sqlserver":
			query = `SELECT uuid FROM artifacts WITH (UPDLOCK, HOLDLOCK) WHERE gateway_id = ? AND kind = ? AND handle = ?`
		}
		err = tx.QueryRowContext(s.ctx, s.bind(query), s.gatewayId, b.ResourceKind, b.Document.Spec.Resource.ID).Scan(&b.ResourceUUID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("budget resource does not exist: %w", ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("resolve budget resource: %w", err)
		}
	}
	if err := s.checkBudgetAPIKeys(tx, budgetKeyRefs(b.Document)); err != nil {
		return err
	}
	now := time.Now().UTC()
	b.GatewayID = s.gatewayId
	b.Revision = 1
	b.CreatedAt, b.UpdatedAt = now, now
	_, err = tx.ExecContext(s.ctx, s.bind(`INSERT INTO budgets
        (uuid, gateway_id, handle, name, version, revision, data_version, status, origin,
         resource_kind, resource_uuid, configuration, created_by, created_at, updated_by, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		b.UUID, b.GatewayID, b.Handle, b.Name, b.Version, b.Revision, b.DataVersion,
		b.Status, string(b.Origin), nullableString(b.ResourceKind), nullableString(b.ResourceUUID),
		string(configuration), b.CreatedBy, now, b.UpdatedBy, now)
	if err != nil {
		if s.isUniqueViolation(err) {
			return fmt.Errorf("budget handle already exists: %w", ErrConflict)
		}
		return fmt.Errorf("insert budget: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit budget: %w", err)
	}
	return nil
}

func budgetKeyRefs(doc models.BudgetDocument) []models.BudgetConsumer {
	var refs []models.BudgetConsumer
	for _, c := range append(append([]models.BudgetConsumer{}, doc.Spec.Consumers...), doc.Spec.Exemptions...) {
		if c.Type == "apiKey" && c.ID != "" && c.Owner != nil {
			refs = append(refs, c)
		}
	}
	return refs
}

func budgetKeyRefID(c models.BudgetConsumer) string {
	if c.Owner == nil {
		return ""
	}
	return c.Owner.Kind + ":" + c.Owner.ID + ":" + c.ID
}

func (s *sqlStore) checkBudgetAPIKeys(tx *sql.Tx, refs []models.BudgetConsumer) error {
	for _, ref := range refs {
		var found string
		query := `SELECT k.uuid FROM api_keys k JOIN artifacts a
            ON a.gateway_id = k.gateway_id AND a.uuid = k.artifact_uuid
            WHERE k.gateway_id = ? AND a.kind = ? AND a.handle = ? AND k.name = ?`
		switch s.backendName {
		case "postgres":
			query += ` FOR UPDATE`
		case "sqlserver":
			query = `SELECT k.uuid FROM api_keys k WITH (UPDLOCK, HOLDLOCK) JOIN artifacts a WITH (UPDLOCK, HOLDLOCK)
                ON a.gateway_id = k.gateway_id AND a.uuid = k.artifact_uuid
                WHERE k.gateway_id = ? AND a.kind = ? AND a.handle = ? AND k.name = ?`
		}
		err := tx.QueryRowContext(s.ctx, s.bind(query), s.gatewayId, ref.Owner.Kind, ref.Owner.ID, ref.ID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("budget API key does not exist: %w", ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("resolve budget API key: %w", err)
		}
	}
	return nil
}

func scanBudget(row interface{ Scan(...any) error }) (*models.StoredBudget, error) {
	var b models.StoredBudget
	var kind, resourceUUID sql.NullString
	var configJSON, origin string
	err := row.Scan(&b.UUID, &b.GatewayID, &b.Handle, &b.Name, &b.Version, &b.Revision,
		&b.DataVersion, &b.Status, &origin, &kind, &resourceUUID, &configJSON,
		&b.CreatedBy, &b.CreatedAt, &b.UpdatedBy, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.Origin = models.Origin(origin)
	b.ResourceKind, b.ResourceUUID = kind.String, resourceUUID.String
	if err := json.Unmarshal([]byte(configJSON), &b.Document); err != nil {
		return nil, fmt.Errorf("decode stored budget %q: %w", b.Handle, err)
	}
	return &b, nil
}

func (s *sqlStore) GetBudget(handle string) (*models.StoredBudget, error) {
	return scanBudget(s.queryRow(`SELECT `+budgetSelectColumns+` FROM budgets WHERE gateway_id = ? AND handle = ?`, s.gatewayId, handle))
}

func (s *sqlStore) ListBudgets() ([]*models.StoredBudget, error) {
	rows, err := s.db.QueryContext(s.ctx, s.bind(`SELECT `+budgetSelectColumns+` FROM budgets WHERE gateway_id = ? ORDER BY handle`), s.gatewayId)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]*models.StoredBudget, 0)
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

// UpdateBudget uses revision as an optimistic guard so concurrent edits cannot
// silently overwrite one another. The original resource binding is untouched.
func (s *sqlStore) UpdateBudget(b *models.StoredBudget) error {
	configuration, err := json.Marshal(b.Document)
	if err != nil {
		return fmt.Errorf("encode budget: %w", err)
	}
	tx, err := s.db.BeginTx(s.ctx, nil)
	if err != nil {
		return fmt.Errorf("begin budget update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Existing target IDs retain their historical binding after deletion. Only
	// newly introduced API-key exemptions need an existence check on PUT.
	var existingJSON string
	err = tx.QueryRowContext(s.ctx, s.bind(`SELECT configuration FROM budgets WHERE gateway_id = ? AND handle = ? AND revision = ?`),
		s.gatewayId, b.Handle, b.Revision).Scan(&existingJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("load existing budget for update: %w", err)
	}
	var existing models.BudgetDocument
	if err := json.Unmarshal([]byte(existingJSON), &existing); err != nil {
		return fmt.Errorf("decode existing budget for update: %w", err)
	}
	known := make(map[string]bool)
	for _, ref := range budgetKeyRefs(existing) {
		known[budgetKeyRefID(ref)] = true
	}
	var newRefs []models.BudgetConsumer
	for _, ref := range budgetKeyRefs(b.Document) {
		if !known[budgetKeyRefID(ref)] {
			newRefs = append(newRefs, ref)
		}
	}
	if err := s.checkBudgetAPIKeys(tx, newRefs); err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(s.ctx, s.bind(`UPDATE budgets SET name = ?, status = ?, configuration = ?,
        revision = revision + 1, updated_by = ?, updated_at = ?
        WHERE gateway_id = ? AND handle = ? AND revision = ?`),
		b.Document.Spec.DisplayName, b.Document.Spec.Status, string(configuration),
		b.UpdatedBy, now, s.gatewayId, b.Handle, b.Revision)
	if err != nil {
		return fmt.Errorf("update budget: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit budget update: %w", err)
	}
	b.Name, b.Status = b.Document.Spec.DisplayName, b.Document.Spec.Status
	b.Revision++
	b.UpdatedAt = now
	return nil
}

func (s *sqlStore) DeleteBudget(handle string) error {
	result, err := s.exec(`DELETE FROM budgets WHERE gateway_id = ? AND handle = ?`, s.gatewayId, handle)
	if err != nil {
		return fmt.Errorf("delete budget: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqlStore) GetBudgetByUUID(id string) (*models.StoredBudget, error) {
	return scanBudget(s.queryRow(`SELECT `+budgetSelectColumns+` FROM budgets WHERE gateway_id = ? AND uuid = ?`, s.gatewayId, id))
}

// UpsertControlPlaneBudget stores a control-plane budget. Named providers and
// API keys are not resolved: a budget reaches every gateway of the organization,
// but a provider is deployed only on some, and there the budget simply never
// matches. A budget created on this gateway's own API with the same handle is
// left alone and reported as a conflict.
func (s *sqlStore) UpsertControlPlaneBudget(b *models.StoredBudget) (bool, error) {
	configuration, err := json.Marshal(b.Document)
	if err != nil {
		return false, fmt.Errorf("encode budget: %w", err)
	}
	tx, err := s.db.BeginTx(s.ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin budget upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var revision int64
	var origin string
	err = tx.QueryRowContext(s.ctx, s.bind(`SELECT revision, origin FROM budgets WHERE gateway_id = ? AND uuid = ?`),
		s.gatewayId, b.UUID).Scan(&revision, &origin)
	now := time.Now().UTC()
	b.GatewayID = s.gatewayId
	b.Origin = models.OriginControlPlane
	switch {
	case errors.Is(err, sql.ErrNoRows):
		b.CreatedAt, b.UpdatedAt = now, now
		_, err = tx.ExecContext(s.ctx, s.bind(`INSERT INTO budgets
            (uuid, gateway_id, handle, name, version, revision, data_version, status, origin,
             resource_kind, resource_uuid, configuration, created_by, created_at, updated_by, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?)`),
			b.UUID, b.GatewayID, b.Handle, b.Name, b.Version, b.Revision, b.DataVersion,
			b.Status, string(b.Origin), nullableString(b.ResourceKind),
			string(configuration), b.CreatedBy, now, b.UpdatedBy, now)
		if err != nil {
			if s.isUniqueViolation(err) {
				return false, fmt.Errorf("a gateway budget already uses handle %q: %w", b.Handle, ErrConflict)
			}
			return false, fmt.Errorf("insert control plane budget: %w", err)
		}
	case err != nil:
		return false, fmt.Errorf("load budget for upsert: %w", err)
	case models.Origin(origin) != models.OriginControlPlane:
		return false, fmt.Errorf("budget %s was not created by the control plane: %w", b.UUID, ErrConflict)
	case b.Revision <= revision:
		return false, nil // a repeated or older event
	default:
		b.UpdatedAt = now
		_, err = tx.ExecContext(s.ctx, s.bind(`UPDATE budgets SET name = ?, status = ?, configuration = ?,
            revision = ?, updated_by = ?, updated_at = ? WHERE gateway_id = ? AND uuid = ?`),
			b.Name, b.Status, string(configuration), b.Revision, b.UpdatedBy, now, s.gatewayId, b.UUID)
		if err != nil {
			return false, fmt.Errorf("update control plane budget: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit control plane budget: %w", err)
	}
	return true, nil
}

func (s *sqlStore) DeleteBudgetByUUID(id string) error {
	result, err := s.exec(`DELETE FROM budgets WHERE gateway_id = ? AND uuid = ?`, s.gatewayId, id)
	if err != nil {
		return fmt.Errorf("delete budget: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
