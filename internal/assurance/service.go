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
			s.now(),
		)
		if err != nil {
			return err
		}
		// A measurement taken under a verified disposition loop is linked to
		// that loop so reviewers can prove it is the follow-up of the same
		// closure rather than an unrelated new measurement.
		measurement.NonconformanceID = item.PendingNonconformanceID
		if err := item.RecordMeasurement(measurement.Accepted, s.now()); err != nil {
			return err
		}
		item.PendingNonconformanceID = ""
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if !measurement.Accepted {
			nonconformance, err := domain.NewNonconformance(
				m.NextID("nonconformance"),
				item.ID,
				measurement.ID,
				s.now(),
			)
			if err != nil {
				return err
			}
			if err := m.AppendNonconformance(nonconformance); err != nil {
				return err
			}
			opened, err := domain.NewAuditEvent(
				m.NextID("event"),
				"nonconformance",
				nonconformance.ID,
				item.ID,
				"nonconformance_opened",
				measurement.Technician,
				"out-of-tolerance measurement opened a disposition loop",
				s.now(),
			)
			if err != nil {
				return err
			}
			m.AppendEvent(opened)
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
		if err := item.Reopen(s.now()); err != nil {
			return err
		}
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		// The case may only continue once its disposition loop is verified:
		// closing the nonconformance here freezes it against later revocation
		// and marks the next measurement as the follow-up of this closure.
		nonconformance, exists := m.OpenNonconformance(item.ID)
		if !exists {
			return fmt.Errorf("%w: case %s has no nonconformance disposition", domain.ErrDispositionIncomplete, item.ID)
		}
		if err := nonconformance.Close(s.now()); err != nil {
			return err
		}
		item.PendingNonconformanceID = nonconformance.ID
		if err := m.UpdateNonconformance(nonconformance); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		closed, err := domain.NewAuditEvent(
			m.NextID("event"),
			"nonconformance",
			nonconformance.ID,
			item.ID,
			"nonconformance_closed",
			actor,
			"verified disposition closed; follow-up measurement will reference this loop",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(closed)
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

func (s *Service) SubmitCorrectiveAction(ctx context.Context, caseID, nonconformanceID string, input contracts.SubmitCorrectiveActionRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		item, nonconformance, err := s.dispositionSubject(m, caseID, nonconformanceID)
		if err != nil {
			return err
		}
		action, err := domain.NewCorrectiveAction(
			m.NextID("action"),
			nonconformance.ID,
			item.ID,
			input.Technician,
			input.Action,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := nonconformance.RecordAction(s.now()); err != nil {
			return err
		}
		if err := m.AppendCorrectiveAction(action); err != nil {
			return err
		}
		if err := m.UpdateNonconformance(nonconformance); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"corrective_action",
			action.ID,
			item.ID,
			"corrective_action_submitted",
			action.Technician,
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

func (s *Service) VerifyCorrectiveAction(ctx context.Context, caseID, nonconformanceID string, input contracts.VerifyCorrectiveActionRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		item, nonconformance, err := s.dispositionSubject(m, caseID, nonconformanceID)
		if err != nil {
			return err
		}
		action, exists := m.ActiveCorrectiveAction(nonconformance.ID)
		if !exists {
			return fmt.Errorf("%w: nonconformance %s has no active corrective action", domain.ErrStateConflict, nonconformance.ID)
		}
		if err := action.Verify(input.Reviewer, input.Basis, s.now()); err != nil {
			return err
		}
		if err := nonconformance.Verify(s.now()); err != nil {
			return err
		}
		if err := m.UpdateCorrectiveAction(action); err != nil {
			return err
		}
		if err := m.UpdateNonconformance(nonconformance); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"corrective_action",
			action.ID,
			item.ID,
			"corrective_action_verified",
			action.Reviewer,
			action.Basis,
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

func (s *Service) RevokeCorrectiveAction(ctx context.Context, caseID, nonconformanceID string, input contracts.RevokeCorrectiveActionRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		item, nonconformance, err := s.dispositionSubject(m, caseID, nonconformanceID)
		if err != nil {
			return err
		}
		action, exists := m.ActiveCorrectiveAction(nonconformance.ID)
		if !exists {
			return fmt.Errorf("%w: nonconformance %s has no active corrective action to revoke", domain.ErrStateConflict, nonconformance.ID)
		}
		// Revocation only marks the action invalid and reopens the disposition
		// loop; the failed measurement and every recorded fact stay untouched.
		if err := nonconformance.ResetForNewAction(s.now()); err != nil {
			return err
		}
		if err := action.Revoke(input.Reviewer, input.Reason, s.now()); err != nil {
			return err
		}
		if err := m.UpdateCorrectiveAction(action); err != nil {
			return err
		}
		if err := m.UpdateNonconformance(nonconformance); err != nil {
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

func (s *Service) Nonconformances(ctx context.Context, caseID string) (contracts.NonconformancesResponse, error) {
	var result contracts.NonconformancesResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		id := strings.TrimSpace(caseID)
		item, exists := snapshot.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = contracts.NonconformancesResponse{
			Nonconformances: nonconformanceViewsFromSnapshot(snapshot, item.ID),
		}
		return nil
	})
	return result, err
}

func (s *Service) dispositionSubject(m *repository.Mutation, caseID, nonconformanceID string) (domain.VerificationCase, domain.Nonconformance, error) {
	id := strings.TrimSpace(caseID)
	item, exists := m.Case(id)
	if !exists {
		return domain.VerificationCase{}, domain.Nonconformance{}, fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
	}
	if item.State == domain.CaseSealed {
		return domain.VerificationCase{}, domain.Nonconformance{}, fmt.Errorf("%w: case %s is sealed and its disposition record is frozen", domain.ErrStateConflict, item.ID)
	}
	nonconformance, exists := m.Nonconformance(item.ID, strings.TrimSpace(nonconformanceID))
	if !exists {
		return domain.VerificationCase{}, domain.Nonconformance{}, fmt.Errorf("%w: nonconformance %s", domain.ErrNotFound, nonconformanceID)
	}
	return item, nonconformance, nil
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

func caseResponse(m *repository.Mutation, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:            item,
		Measurements:    m.Measurements(item.ID),
		Nonconformances: nonconformanceViews(m, item.ID),
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
		Nonconformances: nonconformanceViewsFromSnapshot(snapshot, item.ID),
		Events:          snapshot.Events(item.ID),
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}

func nonconformanceViews(m *repository.Mutation, caseID string) []contracts.NonconformanceResponse {
	items := m.Nonconformances(caseID)
	views := make([]contracts.NonconformanceResponse, 0, len(items))
	for _, item := range items {
		views = append(views, contracts.NonconformanceResponse{
			Nonconformance:    item,
			CorrectiveActions: m.CorrectiveActions(item.ID),
		})
	}
	return views
}

func nonconformanceViewsFromSnapshot(snapshot repository.Snapshot, caseID string) []contracts.NonconformanceResponse {
	items := snapshot.Nonconformances(caseID)
	views := make([]contracts.NonconformanceResponse, 0, len(items))
	for _, item := range items {
		views = append(views, contracts.NonconformanceResponse{
			Nonconformance:    item,
			CorrectiveActions: snapshot.CorrectiveActions(item.ID),
		})
	}
	return views
}
