package domain

import (
	"fmt"
	"strings"
	"time"
)

type NonconformanceState string

const (
	NonconformanceOpen            NonconformanceState = "open"
	NonconformanceActionSubmitted NonconformanceState = "action_submitted"
	NonconformanceVerified        NonconformanceState = "verified"
	NonconformanceClosed          NonconformanceState = "closed"
)

// Nonconformance is the traceable disposition record opened by an
// out-of-tolerance measurement. It links the failed measurement to the
// corrective action loop that must be verified before the case may continue.
type Nonconformance struct {
	ID            string              `json:"id"`
	CaseID        string              `json:"case_id"`
	MeasurementID string              `json:"measurement_id"`
	State         NonconformanceState `json:"state"`
	OpenedAt      time.Time           `json:"opened_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	ClosedAt      *time.Time          `json:"closed_at,omitempty"`
}

func NewNonconformance(id, caseID, measurementID string, at time.Time) (Nonconformance, error) {
	item := Nonconformance{
		ID:            strings.TrimSpace(id),
		CaseID:        strings.TrimSpace(caseID),
		MeasurementID: strings.TrimSpace(measurementID),
		State:         NonconformanceOpen,
		OpenedAt:      at.UTC(),
		UpdatedAt:     at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return Nonconformance{}, err
	}
	return item, nil
}

func (n Nonconformance) Validate() error {
	if n.ID == "" || n.CaseID == "" || n.MeasurementID == "" {
		return fmt.Errorf("%w: nonconformance id, case id, and measurement id are required", ErrInvalidInput)
	}
	if n.OpenedAt.IsZero() || n.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: nonconformance timestamps are required", ErrInvalidInput)
	}
	switch n.State {
	case NonconformanceOpen, NonconformanceActionSubmitted, NonconformanceVerified, NonconformanceClosed:
		return nil
	default:
		return fmt.Errorf("%w: unsupported nonconformance state %q", ErrInvalidInput, n.State)
	}
}

func (n Nonconformance) IsClosed() bool {
	return n.State == NonconformanceClosed
}

func (n *Nonconformance) RecordAction(at time.Time) error {
	if n.State == NonconformanceClosed {
		return fmt.Errorf("%w: nonconformance %s is closed", ErrStateConflict, n.ID)
	}
	if n.State != NonconformanceOpen {
		return fmt.Errorf("%w: nonconformance %s", ErrCorrectiveActionExists, n.ID)
	}
	n.State = NonconformanceActionSubmitted
	n.UpdatedAt = at.UTC()
	return nil
}

func (n *Nonconformance) Verify(at time.Time) error {
	if n.State != NonconformanceActionSubmitted {
		return fmt.Errorf("%w: nonconformance %s has no submitted corrective action to verify", ErrStateConflict, n.ID)
	}
	n.State = NonconformanceVerified
	n.UpdatedAt = at.UTC()
	return nil
}

func (n *Nonconformance) ResetForNewAction(at time.Time) error {
	if n.State == NonconformanceClosed {
		return fmt.Errorf("%w: nonconformance %s is closed", ErrStateConflict, n.ID)
	}
	if n.State == NonconformanceOpen {
		return fmt.Errorf("%w: nonconformance %s has no corrective action to revoke", ErrStateConflict, n.ID)
	}
	n.State = NonconformanceOpen
	n.UpdatedAt = at.UTC()
	return nil
}

func (n *Nonconformance) Close(at time.Time) error {
	if n.State != NonconformanceVerified {
		return fmt.Errorf("%w: nonconformance %s requires a verified corrective action", ErrDispositionIncomplete, n.ID)
	}
	stamp := at.UTC()
	n.State = NonconformanceClosed
	n.UpdatedAt = stamp
	n.ClosedAt = &stamp
	return nil
}
