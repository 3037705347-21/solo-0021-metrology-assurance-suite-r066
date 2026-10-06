package contracts

import "example.com/solo-0021-metrology-assurance-suite/internal/domain"

type RegisterDeviceRequest struct {
	AssetTag string `json:"asset_tag"`
	Model    string `json:"model"`
	Room     string `json:"room"`
	Steward  string `json:"steward"`
}

type OpenCaseRequest struct {
	DeviceID  string  `json:"device_id"`
	Nominal   float64 `json:"nominal"`
	Tolerance float64 `json:"tolerance"`
	Steward   string  `json:"steward"`
}

type RecordMeasurementRequest struct {
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
}

type ReopenCaseRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

type SealCaseRequest struct {
	Reviewer string `json:"reviewer"`
	Summary  string `json:"summary"`
}

type RecordCorrectiveActionRequest struct {
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Rationale string `json:"rationale"`
}

type DispositionNonconformanceRequest struct {
	Reviewer string `json:"reviewer"`
	Basis    string `json:"basis"`
}

type RevokeCorrectiveActionRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

type DeviceResponse struct {
	Device domain.Device `json:"device"`
}

// NonconformanceView bundles a disposition record with its corrective actions
// so reviewers can see the full closure trail in one read.
type NonconformanceView struct {
	Nonconformance domain.Nonconformance     `json:"nonconformance"`
	Actions        []domain.CorrectiveAction `json:"actions"`
}

type CaseResponse struct {
	Case            domain.VerificationCase `json:"case"`
	Measurements    []domain.Measurement    `json:"measurements"`
	Nonconformances []NonconformanceView    `json:"nonconformances"`
	Seal            *domain.EvidenceSeal    `json:"seal,omitempty"`
	Events          []domain.AuditEvent     `json:"events"`
}

type NonconformanceListResponse struct {
	Nonconformances []NonconformanceView `json:"nonconformances"`
}

type EventsResponse struct {
	Events []domain.AuditEvent `json:"events"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
