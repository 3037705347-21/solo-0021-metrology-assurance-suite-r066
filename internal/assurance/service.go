package assurance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type Service struct {
	repository *repository.Repository
	now        func() time.Time
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		repository: repo,
		now:        time.Now,
	}
}

func (s *Service) RegisterDevice(ctx context.Context, input contracts.RegisterDeviceRequest) (domain.Device, error) {
	var created domain.Device
	tag := domain.NormalizeAssetTag(input.AssetTag)
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		if _, exists := m.DeviceByTag(tag); exists {
			return fmt.Errorf("%w: %s", domain.ErrDuplicateAssetTag, tag)
		}
		item, err := domain.NewDevice(
			m.NextID("device"),
			input.AssetTag,
			input.Model,
			input.Room,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutDevice(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			item.ID,
			"",
			"device_enrolled",
			item.Steward,
			"device enrolled for assurance work",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		created = item
		return nil
	})
	return created, err
}

func (s *Service) Device(ctx context.Context, id string) (domain.Device, error) {
	var found domain.Device
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Device(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

func (s *Service) OpenCase(ctx context.Context, input contracts.OpenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		deviceID := strings.TrimSpace(input.DeviceID)
		device, exists := m.Device(deviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, deviceID)
		}
		if !device.IsActive() {
			return fmt.Errorf("%w: device %s is %s", domain.ErrStateConflict, device.ID, device.Status)
		}
		if active, exists := m.ActiveCaseForDevice(device.ID); exists {
			return fmt.Errorf("%w: case %s is still %s", domain.ErrActiveCaseExists, active.ID, active.State)
		}
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			input.Nominal,
			input.Tolerance,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_opened",
			item.Steward,
			"verification case opened",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = caseResponseFromSnapshot(snapshot, item)
		return nil
	})
	return result, err
}

func (s *Service) RecordMeasurement(ctx context.Context, caseID string, input contracts.RecordMeasurementRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			input.Observed,
			item.Nominal,
			item.Tolerance,
			input.Technician,
			pendingDispositionLink(m, item.ID),
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := item.RecordMeasurement(measurement.Accepted, s.now()); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		kind := "measurement_rejected"
		detail := "measurement exceeded tolerance and requires another measurement"
		if measurement.Accepted {
			kind = "measurement_accepted"
			detail = "measurement is within tolerance and ready for review"
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			kind,
			measurement.Technician,
			detail,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		if !measurement.Accepted {
			nonconformance, err := domain.NewNonconformance(
				m.NextID("nonconformance"),
				item.ID,
				measurement.ID,
				measurement.Technician,
				s.now(),
			)
			if err != nil {
				return err
			}
			if err := m.PutNonconformance(nonconformance); err != nil {
				return err
			}
			ncEvent, err := domain.NewAuditEvent(
				m.NextID("event"),
				"nonconformance",
				nonconformance.ID,
				item.ID,
				"nonconformance_opened",
				nonconformance.OpenedBy,
				"out-of-tolerance measurement opened a nonconformance for disposition",
				s.now(),
			)
			if err != nil {
				return err
			}
			m.AppendEvent(ncEvent)
		}
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if item.State == domain.CaseRecheckRequired {
			latest, found := latestNonconformance(m.Nonconformances(item.ID))
			if !found {
				return fmt.Errorf("%w: case %s has no nonconformance to disposition", domain.ErrStateConflict, item.ID)
			}
			if latest.IsOpen() {
				return fmt.Errorf("%w: nonconformance %s must be dispositioned before reopening", domain.ErrStateConflict, latest.ID)
			}
		}
		if err := item.Reopen(s.now()); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_reopened",
			actor,
			reason,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) RecordCorrectiveAction(ctx context.Context, caseID, nonconformanceID string, input contracts.RecordCorrectiveActionRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		item, nonconformance, err := nonconformanceForCase(m, caseID, nonconformanceID)
		if err != nil {
			return err
		}
		if !nonconformance.IsOpen() {
			return fmt.Errorf("%w: nonconformance %s is already dispositioned", domain.ErrStateConflict, nonconformance.ID)
		}
		action, err := domain.NewCorrectiveAction(
			m.NextID("action"),
			nonconformance.ID,
			item.ID,
			input.Actor,
			input.Action,
			input.Rationale,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.AppendCorrectiveAction(action); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"corrective_action",
			action.ID,
			item.ID,
			"corrective_action_recorded",
			action.Actor,
			action.Action,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) DispositionNonconformance(ctx context.Context, caseID, nonconformanceID string, input contracts.DispositionNonconformanceRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		item, nonconformance, err := nonconformanceForCase(m, caseID, nonconformanceID)
		if err != nil {
			return err
		}
		if !hasActiveCorrectiveAction(m.CorrectiveActions(nonconformance.ID)) {
			return fmt.Errorf("%w: an active corrective action is required before disposition", domain.ErrStateConflict)
		}
		if err := nonconformance.Disposition(input.Reviewer, input.Basis, s.now()); err != nil {
			return err
		}
		if err := m.UpdateNonconformance(nonconformance); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"nonconformance",
			nonconformance.ID,
			item.ID,
			"nonconformance_dispositioned",
			nonconformance.DispositionedBy,
			nonconformance.DispositionBasis,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) RevokeCorrectiveAction(ctx context.Context, caseID, nonconformanceID, actionID string, input contracts.RevokeCorrectiveActionRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		item, nonconformance, err := nonconformanceForCase(m, caseID, nonconformanceID)
		if err != nil {
			return err
		}
		if !nonconformance.IsOpen() {
			return fmt.Errorf("%w: nonconformance %s is already dispositioned", domain.ErrStateConflict, nonconformance.ID)
		}
		action, found := findCorrectiveAction(m.CorrectiveActions(nonconformance.ID), actionID)
		if !found {
			return fmt.Errorf("%w: corrective action %s", domain.ErrNotFound, strings.TrimSpace(actionID))
		}
		if err := action.Revoke(input.Actor, input.Reason, s.now()); err != nil {
			return err
		}
		if err := m.UpdateCorrectiveAction(action); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"corrective_action",
			action.ID,
			item.ID,
			"corrective_action_revoked",
			action.RevokedBy,
			action.RevokeReason,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) Nonconformances(ctx context.Context, caseID string) (contracts.NonconformanceListResponse, error) {
	var result contracts.NonconformanceListResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		id := strings.TrimSpace(caseID)
		if _, exists := snapshot.Case(id); !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = contracts.NonconformanceListResponse{
			Nonconformances: nonconformanceViews(snapshot.Nonconformances(id), snapshot.CorrectiveActions),
		}
		return nil
	})
	return result, err
}

func (s *Service) Nonconformance(ctx context.Context, caseID, nonconformanceID string) (contracts.NonconformanceView, error) {
	var result contracts.NonconformanceView
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		id := strings.TrimSpace(caseID)
		if _, exists := snapshot.Case(id); !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		nonconformance, exists := snapshot.Nonconformance(strings.TrimSpace(nonconformanceID))
		if !exists || nonconformance.CaseID != id {
			return fmt.Errorf("%w: nonconformance %s", domain.ErrNotFound, nonconformanceID)
		}
		result = contracts.NonconformanceView{
			Nonconformance: nonconformance,
			Actions:        snapshot.CorrectiveActions(nonconformance.ID),
		}
		return nil
	})
	return result, err
}

func (s *Service) SealCase(ctx context.Context, caseID string, input contracts.SealCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if _, exists := m.Seal(item.ID); exists {
			return fmt.Errorf("%w: case %s", domain.ErrSealAlreadyCreated, item.ID)
		}
		measurement, ok := bestAcceptedMeasurement(m.Measurements(item.ID))
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
		}
		if err := item.Seal(s.now()); err != nil {
			return err
		}
		seal, err := domain.NewEvidenceSeal(
			m.NextID("seal"),
			item.ID,
			item.DeviceID,
			measurement.ID,
			input.Reviewer,
			input.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_sealed",
			seal.Reviewer,
			seal.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func bestAcceptedMeasurement(items []domain.Measurement) (domain.Measurement, bool) {
	var selected domain.Measurement
	found := false
	for _, item := range items {
		if !item.Accepted {
			continue
		}
		if !found || item.AbsoluteDeviation() < selected.AbsoluteDeviation() {
			selected = item
			found = true
		}
	}
	return selected, found
}

func nonconformanceForCase(m *repository.Mutation, caseID, nonconformanceID string) (domain.VerificationCase, domain.Nonconformance, error) {
	id := strings.TrimSpace(caseID)
	item, exists := m.Case(id)
	if !exists {
		return domain.VerificationCase{}, domain.Nonconformance{}, fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
	}
	nonconformance, exists := m.Nonconformance(strings.TrimSpace(nonconformanceID))
	if !exists || nonconformance.CaseID != item.ID {
		return domain.VerificationCase{}, domain.Nonconformance{}, fmt.Errorf("%w: nonconformance %s", domain.ErrNotFound, nonconformanceID)
	}
	return item, nonconformance, nil
}

func latestNonconformance(items []domain.Nonconformance) (domain.Nonconformance, bool) {
	if len(items) == 0 {
		return domain.Nonconformance{}, false
	}
	return items[len(items)-1], true
}

func hasActiveCorrectiveAction(items []domain.CorrectiveAction) bool {
	for _, item := range items {
		if item.IsActive() {
			return true
		}
	}
	return false
}

func findCorrectiveAction(items []domain.CorrectiveAction, id string) (domain.CorrectiveAction, bool) {
	trimmed := strings.TrimSpace(id)
	for _, item := range items {
		if item.ID == trimmed {
			return item, true
		}
	}
	return domain.CorrectiveAction{}, false
}

// pendingDispositionLink returns the id of the dispositioned nonconformance
// whose reopen loop has not produced a re-measurement yet. Linking it to the
// next measurement proves the new value closes that disposition loop instead
// of appearing out of nowhere.
func pendingDispositionLink(m *repository.Mutation, caseID string) string {
	latest, found := latestNonconformance(m.Nonconformances(caseID))
	if !found || latest.IsOpen() {
		return ""
	}
	for _, measurement := range m.Measurements(caseID) {
		if measurement.NonconformanceID == latest.ID {
			return ""
		}
	}
	return latest.ID
}

func nonconformanceViews(items []domain.Nonconformance, actions func(string) []domain.CorrectiveAction) []contracts.NonconformanceView {
	var result []contracts.NonconformanceView
	for _, item := range items {
		result = append(result, contracts.NonconformanceView{
			Nonconformance: item,
			Actions:        actions(item.ID),
		})
	}
	return result
}

func caseResponse(m *repository.Mutation, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:            item,
		Measurements:    m.Measurements(item.ID),
		Nonconformances: nonconformanceViews(m.Nonconformances(item.ID), m.CorrectiveActions),
		Events:          m.Events(item.ID),
	}
	if seal, exists := m.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}

func caseResponseFromSnapshot(snapshot repository.Snapshot, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:            item,
		Measurements:    snapshot.Measurements(item.ID),
		Nonconformances: nonconformanceViews(snapshot.Nonconformances(item.ID), snapshot.CorrectiveActions),
		Events:          snapshot.Events(item.ID),
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}
