package domain

import (
	"fmt"
	"strings"
	"time"
)

type CorrectiveActionStatus string

const (
	CorrectiveActionActive  CorrectiveActionStatus = "active"
	CorrectiveActionRevoked CorrectiveActionStatus = "revoked"
)

// CorrectiveAction is one recorded field measure plus its rationale, attached
// to a nonconformance. Revocation flips the status but keeps the original
// record, so the disposition trail stays append-only.
type CorrectiveAction struct {
	ID               string                 `json:"id"`
	NonconformanceID string                 `json:"nonconformance_id"`
	CaseID           string                 `json:"case_id"`
	Actor            string                 `json:"actor"`
	Action           string                 `json:"action"`
	Rationale        string                 `json:"rationale"`
	Status           CorrectiveActionStatus `json:"status"`
	CreatedAt        time.Time              `json:"created_at"`
	RevokedBy        string                 `json:"revoked_by,omitempty"`
	RevokeReason     string                 `json:"revoke_reason,omitempty"`
	RevokedAt        *time.Time             `json:"revoked_at,omitempty"`
}

func NewCorrectiveAction(id, nonconformanceID, caseID, actor, action, rationale string, at time.Time) (CorrectiveAction, error) {
	item := CorrectiveAction{
		ID:               strings.TrimSpace(id),
		NonconformanceID: strings.TrimSpace(nonconformanceID),
		CaseID:           strings.TrimSpace(caseID),
		Actor:            strings.TrimSpace(actor),
		Action:           strings.TrimSpace(action),
		Rationale:        strings.TrimSpace(rationale),
		Status:           CorrectiveActionActive,
		CreatedAt:        at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return CorrectiveAction{}, err
	}
	return item, nil
}

func (a CorrectiveAction) Validate() error {
	if a.ID == "" || a.NonconformanceID == "" || a.CaseID == "" || a.Actor == "" {
		return fmt.Errorf("%w: corrective action id, nonconformance id, case id, and actor are required", ErrInvalidInput)
	}
	if a.Action == "" || a.Rationale == "" {
		return fmt.Errorf("%w: corrective action and rationale are required", ErrInvalidInput)
	}
	if a.CreatedAt.IsZero() {
		return fmt.Errorf("%w: corrective action time is required", ErrInvalidInput)
	}
	switch a.Status {
	case CorrectiveActionActive:
		if a.RevokedBy != "" || a.RevokeReason != "" || a.RevokedAt != nil {
			return fmt.Errorf("%w: an active corrective action cannot carry revocation facts", ErrInvalidInput)
		}
		return nil
	case CorrectiveActionRevoked:
		if a.RevokedBy == "" || a.RevokeReason == "" || a.RevokedAt == nil {
			return fmt.Errorf("%w: a revoked corrective action requires revoker, reason, and time", ErrInvalidInput)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported corrective action status %q", ErrInvalidInput, a.Status)
	}
}

func (a CorrectiveAction) IsActive() bool {
	return a.Status == CorrectiveActionActive
}

// Revoke marks the action invalid without erasing it. The original action and
// rationale stay intact for the audit trail.
func (a *CorrectiveAction) Revoke(actor, reason string, at time.Time) error {
	if a.Status != CorrectiveActionActive {
		return fmt.Errorf("%w: corrective action %s is not active", ErrStateConflict, a.ID)
	}
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if actor == "" || reason == "" {
		return fmt.Errorf("%w: revoker and reason are required", ErrInvalidInput)
	}
	stamp := at.UTC()
	a.Status = CorrectiveActionRevoked
	a.RevokedBy = actor
	a.RevokeReason = reason
	a.RevokedAt = &stamp
	return nil
}
