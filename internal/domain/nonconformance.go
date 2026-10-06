package domain

import (
	"fmt"
	"strings"
	"time"
)

type NonconformanceStatus string

const (
	NonconformanceOpen          NonconformanceStatus = "open"
	NonconformanceDispositioned NonconformanceStatus = "dispositioned"
)

// Nonconformance is the traceable disposition record opened by an
// out-of-tolerance measurement. It never rewrites the failed measurement;
// it only accumulates disposition facts around it.
type Nonconformance struct {
	ID               string               `json:"id"`
	CaseID           string               `json:"case_id"`
	MeasurementID    string               `json:"measurement_id"`
	Status           NonconformanceStatus `json:"status"`
	OpenedBy         string               `json:"opened_by"`
	OpenedAt         time.Time            `json:"opened_at"`
	DispositionedBy  string               `json:"dispositioned_by,omitempty"`
	DispositionBasis string               `json:"disposition_basis,omitempty"`
	DispositionedAt  *time.Time           `json:"dispositioned_at,omitempty"`
	Version          int                  `json:"version"`
}

func NewNonconformance(id, caseID, measurementID, openedBy string, at time.Time) (Nonconformance, error) {
	item := Nonconformance{
		ID:            strings.TrimSpace(id),
		CaseID:        strings.TrimSpace(caseID),
		MeasurementID: strings.TrimSpace(measurementID),
		Status:        NonconformanceOpen,
		OpenedBy:      strings.TrimSpace(openedBy),
		OpenedAt:      at.UTC(),
		Version:       1,
	}
	if err := item.Validate(); err != nil {
		return Nonconformance{}, err
	}
	return item, nil
}

func (n Nonconformance) Validate() error {
	if n.ID == "" || n.CaseID == "" || n.MeasurementID == "" || n.OpenedBy == "" {
		return fmt.Errorf("%w: nonconformance id, case id, measurement id, and opener are required", ErrInvalidInput)
	}
	if n.Version < 1 || n.OpenedAt.IsZero() {
		return fmt.Errorf("%w: nonconformance version and opened time are required", ErrInvalidInput)
	}
	switch n.Status {
	case NonconformanceOpen:
		if n.DispositionedBy != "" || n.DispositionBasis != "" || n.DispositionedAt != nil {
			return fmt.Errorf("%w: an open nonconformance cannot carry disposition facts", ErrInvalidInput)
		}
		return nil
	case NonconformanceDispositioned:
		if n.DispositionedBy == "" || n.DispositionBasis == "" || n.DispositionedAt == nil {
			return fmt.Errorf("%w: a dispositioned nonconformance requires reviewer, basis, and time", ErrInvalidInput)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported nonconformance status %q", ErrInvalidInput, n.Status)
	}
}

func (n Nonconformance) IsOpen() bool {
	return n.Status == NonconformanceOpen
}

// Disposition closes the review loop with the verifier's basis. It is the
// only way a nonconformance leaves the open state.
func (n *Nonconformance) Disposition(reviewer, basis string, at time.Time) error {
	if n.Status != NonconformanceOpen {
		return fmt.Errorf("%w: nonconformance %s is already dispositioned", ErrStateConflict, n.ID)
	}
	reviewer = strings.TrimSpace(reviewer)
	basis = strings.TrimSpace(basis)
	if reviewer == "" || basis == "" {
		return fmt.Errorf("%w: reviewer and basis are required", ErrInvalidInput)
	}
	stamp := at.UTC()
	n.Status = NonconformanceDispositioned
	n.DispositionedBy = reviewer
	n.DispositionBasis = basis
	n.DispositionedAt = &stamp
	n.Version++
	return nil
}
