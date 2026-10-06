# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, opens verification cases,
records measurements, opens a nonconformance disposition record for failed
tolerance decisions, tracks corrective actions and the review basis, and seals
traceable evidence records.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow nonconformance-corrective-action
go run ./cmd/workflowcheck --workflow seal-verification
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command, not a unit test.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add the unit and integration test suite and replace
or extend these smoke checks with red/green verification tests.

## API

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`
- `GET /cases/{id}/nonconformances`
- `GET /cases/{id}/nonconformances/{ncID}`
- `POST /cases/{id}/nonconformances/{ncID}/actions`
- `POST /cases/{id}/nonconformances/{ncID}/disposition`
- `POST /cases/{id}/nonconformances/{ncID}/actions/{actionID}/revoke`

An out-of-tolerance measurement moves the case to `recheck_required` and opens
a nonconformance bound to the failed measurement. The technician records
corrective actions with their rationale, and the verifier dispositions the
nonconformance with a review basis once at least one action is active. Only
then can the case reopen; the next measurement is linked to the
nonconformance it closes, so the passing value is provably the result of the
same disposition loop. Revoking an invalid action marks it `revoked` without
erasing it, and none of these steps can rewrite the original failed
measurement or a sealed case.

## Layout

- `cmd/metrology-service`: executable HTTP server.
- `cmd/workflowcheck`: bounded workflow verification commands.
- `internal/httpapi`: route and JSON boundary.
- `internal/assurance`: workflow orchestration.
- `internal/domain`: state rules and invariants.
- `internal/repository`: copy-on-write in-memory persistence.
- `internal/contracts`: API request and response models.
- `internal/bootstrap`: concrete dependency wiring.

## Inputs And Outputs

Inputs are JSON HTTP requests. Outputs are JSON response envelopes with either a
resource view or a stable error object:

```json
{
  "error": {
    "code": "state_conflict",
    "message": "case is not awaiting measurement"
  }
}
```

The service has no environment variables and no external dependencies.
