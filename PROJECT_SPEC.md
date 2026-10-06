# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, opens a
verification case for one device, records technician measurements, opens a
nonconformance disposition record when tolerance is exceeded, tracks corrective
actions and the review basis, requests a repeat measurement when the
disposition is complete, and seals the final evidence record after review.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices and opens verification cases.
- Measurement technician: records observed values for an open case and
  documents corrective actions with their rationale.
- Quality verifier: dispositions nonconformances with a review basis, reviews
  accepted measurements, and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- VerificationCase: one bounded assurance activity for a device, with a nominal
  value, tolerance, current state, version, and assigned steward.
- Measurement: an observed value, computed deviation, acceptance decision,
  technician, and timestamp. A re-measurement that closes a disposition loop
  carries the id of the nonconformance it answers.
- Nonconformance: the traceable disposition record opened by an
  out-of-tolerance measurement, with status, opener, and — once dispositioned —
  reviewer, review basis, and disposition time.
- CorrectiveAction: one field measure plus its rationale, recorded against a
  nonconformance. Revocation marks it invalid without erasing the record.
- EvidenceSeal: the immutable closing record for a reviewed case, including the
  selected accepted measurement.
- AuditEvent: an append-only trace of meaningful case and device changes.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Open Verification Case

The steward sends `POST /cases` with a device ID, nominal value, tolerance, and
owner. The service verifies that the device exists, is active, and has no
unfinished case. A valid request creates a case in `awaiting_measurement`.

### 3. Record Measurement

A technician sends `POST /cases/{id}/measurements`. The case must be
`awaiting_measurement`. The service computes the signed deviation from the
nominal value. An in-tolerance measurement moves the case to
`review_ready`; an out-of-tolerance measurement moves it to
`recheck_required` and opens a nonconformance bound to the failed
measurement in the same commit. A measurement recorded after a dispositioned
loop is linked to that nonconformance, so reviewers can prove the new value
closes the same disposition loop.

### 4. Record Corrective Action

A technician sends `POST /cases/{id}/nonconformances/{ncID}/actions` with an
actor, the field action taken, and its rationale. The nonconformance must be
`open`. Each action starts `active`.

### 5. Revoke Corrective Action

An invalid action is retired with
`POST /cases/{id}/nonconformances/{ncID}/actions/{actionID}/revoke`, naming the
actor and reason. Only `active` actions on an `open` nonconformance can be
revoked. Revocation flips the status to `revoked` and keeps the original
action, rationale, and failed measurement untouched.

### 6. Disposition Nonconformance

The verifier sends `POST /cases/{id}/nonconformances/{ncID}/disposition` with a
reviewer and a review basis. The nonconformance must be `open` and have at
least one active corrective action. A successful disposition moves it to
`dispositioned`; concurrent or repeated disposition attempts conflict and
change nothing.

### 7. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance result.
Only `recheck_required` cases whose latest nonconformance is `dispositioned`
may move back to `awaiting_measurement`. The service records the actor and
reason in the audit trail.

### 8. Seal Verification

The verifier sends `POST /cases/{id}/seal` after an accepted measurement. Only
`review_ready` cases may be sealed. The service selects the accepted
measurement with the smallest absolute deviation, creates exactly one evidence
seal, moves the case to `sealed`, and rejects later mutations.

## State And Rules

Device state:

- `active -> retired`
- `retired` is terminal.

VerificationCase state:

- `awaiting_measurement -> review_ready`
- `awaiting_measurement -> recheck_required`
- `recheck_required -> awaiting_measurement`
- `review_ready -> sealed`
- `sealed` is terminal.

Nonconformance state:

- `open -> dispositioned`
- `dispositioned` is terminal.

CorrectiveAction state:

- `active -> revoked`
- `revoked` is terminal.

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- Measurements are accepted only while a case is awaiting measurement.
- A tolerance must be positive.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, or seal again.
- Every out-of-tolerance measurement opens exactly one nonconformance in the
  same repository commit.
- A nonconformance can be dispositioned only while open and only with at
  least one active corrective action.
- A recheck_required case can reopen only after its latest nonconformance is
  dispositioned.
- The first measurement after a dispositioned loop links to that
  nonconformance id.
- Recorded measurements and evidence seals are never rewritten; corrective
  action revocation is append-only and cannot alter them.
- Every mutation that changes a case also appends an audit event in the same
  repository commit.

## Modules

- `cmd/metrology-service`: process entrypoint and graceful HTTP shutdown.
- `cmd/workflowcheck`: bounded smoke checks that call the public HTTP API.
- `internal/httpapi`: routing, request decoding, and stable error mapping.
- `internal/assurance`: workflow orchestration and atomic decisions.
- `internal/domain`: entities, validation, state transitions, and errors.
- `internal/repository`: mutex-protected in-memory persistence with copy-on-write
  commits.
- `internal/contracts`: request and response contracts shared by the HTTP layer.

## Public Interfaces

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

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The seal check races two concurrent seal
requests to prove that the repository mutation lock permits exactly one seal.
The nonconformance check races two concurrent disposition requests the same
way, and proves that duplicate submissions, revocation, and the completed
disposition loop never rewrite the original failed measurement or the seal.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
