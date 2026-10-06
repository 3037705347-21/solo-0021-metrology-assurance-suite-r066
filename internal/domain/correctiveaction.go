package domain

import (
	"fmt"
	"strings"
	"time"
)

type CorrectiveActionState string

const (
	CorrectiveActionSubmitted CorrectiveActionState = "submitted"
	CorrectiveActionVerified  CorrectiveActionState = "verified"
	CorrectiveActionRevoked   CorrectiveActionState = "revoked"
)

// CorrectiveAction records the on-site disposition of a nonconformance: the
// technician's action, the reviewer's verification basis, and a possible
// revocation. Revoked actions are kept for the audit trail; they are never
// deleted or rewritten.
type CorrectiveAction struct {
	ID               string                `json:"id"`
	NonconformanceID string                `json:"nonconformance_id"`
	CaseID           string                `json:"case_id"`
	Technician       string                `json:"technician"`
	Action           string                `json:"action"`
	State            CorrectiveActionState `json:"state"`
	Reviewer         string                `json:"reviewer,omitempty"`
	Basis            string                `json:"basis,omitempty"`
	RevokedBy        string                `json:"revoked_by,omitempty"`
	RevokeReason     string                `json:"revoke_reason,omitempty"`
	SubmittedAt      time.Time             `json:"submitted_at"`
	VerifiedAt       *time.Time            `json:"verified_at,omitempty"`
	RevokedAt        *time.Time            `json:"revoked_at,omitempty"`
}

func NewCorrectiveAction(id, nonconformanceID, caseID, technician, action string, at time.Time) (CorrectiveAction, error) {
	item := CorrectiveAction{
		ID:               strings.TrimSpace(id),
		NonconformanceID: strings.TrimSpace(nonconformanceID),
		CaseID:           strings.TrimSpace(caseID),
		Technician:       strings.TrimSpace(technician),
		Action:           strings.TrimSpace(action),
		State:            CorrectiveActionSubmitted,
		SubmittedAt:      at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return CorrectiveAction{}, err
	}
	return item, nil
}

func (a CorrectiveAction) Validate() error {
	if a.ID == "" || a.NonconformanceID == "" || a.CaseID == "" || a.Technician == "" || a.Action == "" {
		return fmt.Errorf("%w: corrective action id, nonconformance, case, technician, and action are required", ErrInvalidInput)
	}
	if a.SubmittedAt.IsZero() {
		return fmt.Errorf("%w: corrective action submission time is required", ErrInvalidInput)
	}
	switch a.State {
	case CorrectiveActionSubmitted, CorrectiveActionVerified, CorrectiveActionRevoked:
		return nil
	default:
		return fmt.Errorf("%w: unsupported corrective action state %q", ErrInvalidInput, a.State)
	}
}

func (a CorrectiveAction) IsActive() bool {
	return a.State != CorrectiveActionRevoked
}

func (a *CorrectiveAction) Verify(reviewer, basis string, at time.Time) error {
	if a.State != CorrectiveActionSubmitted {
		return fmt.Errorf("%w: corrective action %s is not awaiting verification", ErrStateConflict, a.ID)
	}
	reviewer = strings.TrimSpace(reviewer)
	basis = strings.TrimSpace(basis)
	if reviewer == "" || basis == "" {
		return fmt.Errorf("%w: reviewer and basis are required", ErrInvalidInput)
	}
	stamp := at.UTC()
	a.State = CorrectiveActionVerified
	a.Reviewer = reviewer
	a.Basis = basis
	a.VerifiedAt = &stamp
	return nil
}

func (a *CorrectiveAction) Revoke(actor, reason string, at time.Time) error {
	if a.State == CorrectiveActionRevoked {
		return fmt.Errorf("%w: corrective action %s is already revoked", ErrStateConflict, a.ID)
	}
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if actor == "" || reason == "" {
		return fmt.Errorf("%w: actor and reason are required", ErrInvalidInput)
	}
	stamp := at.UTC()
	a.State = CorrectiveActionRevoked
	a.RevokedBy = actor
	a.RevokeReason = reason
	a.RevokedAt = &stamp
	return nil
}
