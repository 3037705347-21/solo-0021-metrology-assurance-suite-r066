# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, opens a
verification case for one device, records technician measurements, opens a
nonconformance disposition loop when tolerance is exceeded, tracks the
corrective action through technician submission and quality verification, and
seals the final evidence record after review.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices and opens verification cases.
- Measurement technician: records observed values for an open case and submits
  corrective actions for a nonconformance.
- Quality verifier: verifies or revokes corrective actions with a recorded
  basis, reviews accepted measurements, and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- VerificationCase: one bounded assurance activity for a device, with a nominal
  value, tolerance, current state, version, and assigned steward.
- Measurement: an observed value, computed deviation, acceptance decision,
  technician, and timestamp. A follow-up measurement taken under a verified
  disposition loop references that nonconformance.
- Nonconformance: the traceable disposition record opened by an
  out-of-tolerance measurement, linking the failed measurement to the
  corrective action loop that must be verified before the case continues.
- CorrectiveAction: the technician's on-site action for a nonconformance, the
  reviewer's verification basis, and a possible revocation. Revoked actions are
  retained, never deleted.
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
`recheck_required` and opens a nonconformance linked to the failed
measurement in the same commit. A measurement recorded after a verified
disposition loop is linked to that nonconformance as its follow-up.

### 4. Disposition Nonconformance

The technician sends `POST /cases/{id}/nonconformances/{ncid}/actions` with
the on-site corrective action. The nonconformance must be `open`; a repeated
or concurrent submission conflicts and records nothing. The verifier sends
`POST /cases/{id}/nonconformances/{ncid}/verify` with a review basis to mark
the action verified, or `POST /cases/{id}/nonconformances/{ncid}/revoke`
with a reason to invalidate it. Revocation returns the nonconformance to
`open` so a replacement action can be submitted; it never rewrites the
failed measurement or any sealed fact. `GET /cases/{id}/nonconformances`
lists the full disposition record.

### 5. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance result.
Only `recheck_required` cases whose nonconformance has a verified corrective
action may move back to `awaiting_measurement`; otherwise the request fails
with `disposition_incomplete`. Reopening closes the nonconformance, records
it as the pending disposition loop for the next measurement, and stores the
actor and reason in the audit trail.

### 6. Seal Verification

The verifier sends `POST /cases/{id}/seal` after an accepted measurement. Only
`review_ready` cases may be sealed. The service selects the accepted
measurement with the smallest absolute deviation, creates exactly one evidence
seal, moves the case to `sealed`, and rejects later mutations, including any
disposition change.

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

- `open -> action_submitted`
- `action_submitted -> verified`
- `action_submitted -> open` (corrective action revoked)
- `verified -> open` (corrective action revoked)
- `verified -> closed` (case reopened for the follow-up measurement)
- `closed` is terminal.

CorrectiveAction state:

- `submitted -> verified`
- `submitted -> revoked`
- `verified -> revoked`
- `revoked` is terminal; the record is retained.

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- Measurements are accepted only while a case is awaiting measurement.
- A tolerance must be positive.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, seal again, or change its
  disposition record.
- Every out-of-tolerance measurement opens exactly one nonconformance in the
  same commit, and a case has at most one unresolved nonconformance.
- A nonconformance has at most one active (non-revoked) corrective action.
- A case may reopen only when its nonconformance holds a verified corrective
  action; the follow-up measurement references that nonconformance.
- Revoking a corrective action never rewrites the failed measurement, other
  corrective actions, or sealed facts.
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
- `POST /cases/{id}/nonconformances/{ncid}/actions`
- `POST /cases/{id}/nonconformances/{ncid}/verify`
- `POST /cases/{id}/nonconformances/{ncid}/revoke`

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The seal check also races two concurrent
seal requests to prove that the repository mutation lock permits exactly one
seal. The disposition check races two corrective action submissions, revokes
and resubmits an action, and proves the failed measurement and sealed facts
survive every disposition change.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
