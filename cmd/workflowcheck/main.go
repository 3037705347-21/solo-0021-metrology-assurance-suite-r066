package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":               checkRegisterDevice,
		"open-verification-case":        checkOpenVerificationCase,
		"record-measurement":            checkRecordMeasurement,
		"reopen-verification-case":      checkReopenVerificationCase,
		"seal-verification":             checkSealVerification,
		"corrective-action-disposition": checkCorrectiveActionDisposition,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	nonconformanceID, err := nestedStringPath(rejected.Body, "nonconformances", 0, "nonconformance", "id")
	if err != nil {
		return err
	}
	if nonconformanceID == "" {
		return errors.New("rejected measurement did not open a nonconformance")
	}
	nonconformanceState, err := nestedStringPath(rejected.Body, "nonconformances", 0, "nonconformance", "state")
	if err != nil {
		return err
	}
	if nonconformanceState != "open" {
		return fmt.Errorf("new nonconformance state = %q", nonconformanceState)
	}
	failedMeasurementID, err := nestedStringPath(rejected.Body, "measurements", 0, "id")
	if err != nil {
		return err
	}
	linkedMeasurementID, err := nestedStringPath(rejected.Body, "nonconformances", 0, "nonconformance", "measurement_id")
	if err != nil {
		return err
	}
	if linkedMeasurementID != failedMeasurementID {
		return fmt.Errorf("nonconformance links measurement %q, expected %q", linkedMeasurementID, failedMeasurementID)
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	nonconformanceID, err := h.nonconformanceID(caseID)
	if err != nil {
		return err
	}

	blocked, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(blocked, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(blocked, "disposition_incomplete"); err != nil {
		return err
	}

	submitted, err := h.submitAction(caseID, nonconformanceID, "Rui", "replaced the measurement setup")
	if err != nil {
		return err
	}
	if err := expectStatus(submitted, http.StatusCreated); err != nil {
		return err
	}
	verifiedSetup, err := h.verifyAction(caseID, nonconformanceID, "Mira", "setup replacement record MET-1301-R1")
	if err != nil {
		return err
	}
	if err := expectStatus(verifiedSetup, http.StatusOK); err != nil {
		return err
	}

	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "verified corrective action authorizes a repeat measurement",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	pending, err := nestedString(reopened.Body, "case", "pending_nonconformance_id")
	if err != nil {
		return err
	}
	if pending != nonconformanceID {
		return fmt.Errorf("pending nonconformance = %q, expected %q", pending, nonconformanceID)
	}
	closedState, err := nestedStringPath(reopened.Body, "nonconformances", 0, "nonconformance", "state")
	if err != nil {
		return err
	}
	if closedState != "closed" {
		return fmt.Errorf("nonconformance state after reopen = %q", closedState)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	if !hasEvent(events.Body, "nonconformance_closed") {
		return errors.New("nonconformance closure audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

func checkCorrectiveActionDisposition() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1501")
	if err != nil {
		return err
	}
	view, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(view, http.StatusOK); err != nil {
		return err
	}
	failedMeasurementID, err := nestedStringPath(view.Body, "measurements", 0, "id")
	if err != nil {
		return err
	}
	nonconformanceID, err := nestedStringPath(view.Body, "nonconformances", 0, "nonconformance", "id")
	if err != nil {
		return err
	}
	linked, err := nestedStringPath(view.Body, "nonconformances", 0, "nonconformance", "measurement_id")
	if err != nil {
		return err
	}
	if linked != failedMeasurementID {
		return fmt.Errorf("nonconformance links measurement %q, expected %q", linked, failedMeasurementID)
	}

	listed, err := h.request(http.MethodGet, "/cases/"+caseID+"/nonconformances", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(listed, http.StatusOK); err != nil {
		return err
	}
	listedID, err := nestedStringPath(listed.Body, "nonconformances", 0, "nonconformance", "id")
	if err != nil {
		return err
	}
	if listedID != nonconformanceID {
		return fmt.Errorf("list returned nonconformance %q, expected %q", listedID, nonconformanceID)
	}

	// Concurrent submissions race for the same loop: exactly one is recorded.
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/nonconformances/"+nonconformanceID+"/actions", map[string]any{
				"technician": fmt.Sprintf("Tech-%d", index+1),
				"action":     "replaced the measurement setup",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)
	created := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent submit status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if created != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent submit outcomes: %d created, %d conflict", created, conflicts)
	}

	// A repeated submission is rejected and does not add another action.
	duplicate, err := h.submitAction(caseID, nonconformanceID, "Rui", "duplicate submission")
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(duplicate, "corrective_action_exists"); err != nil {
		return err
	}

	missingBasis, err := h.request(http.MethodPost, "/cases/"+caseID+"/nonconformances/"+nonconformanceID+"/verify", map[string]any{
		"reviewer": "Mira",
		"basis":    "  ",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(missingBasis, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(missingBasis, "invalid_input"); err != nil {
		return err
	}

	verified, err := h.verifyAction(caseID, nonconformanceID, "Mira", "setup replacement record MET-1501-R1")
	if err != nil {
		return err
	}
	if err := expectStatus(verified, http.StatusOK); err != nil {
		return err
	}
	verifiedState, err := nestedStringPath(verified.Body, "nonconformances", 0, "nonconformance", "state")
	if err != nil {
		return err
	}
	if verifiedState != "verified" {
		return fmt.Errorf("verified nonconformance state = %q", verifiedState)
	}
	actionID, err := nestedStringPath(verified.Body, "nonconformances", 0, "corrective_actions", 0, "id")
	if err != nil {
		return err
	}
	basis, err := nestedStringPath(verified.Body, "nonconformances", 0, "corrective_actions", 0, "basis")
	if err != nil {
		return err
	}
	if basis != "setup replacement record MET-1501-R1" {
		return fmt.Errorf("verification basis = %q", basis)
	}

	reverify, err := h.verifyAction(caseID, nonconformanceID, "Mira", "second verification")
	if err != nil {
		return err
	}
	if err := expectStatus(reverify, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(reverify, "state_conflict"); err != nil {
		return err
	}

	// Revoking an invalid action reopens the loop without touching history.
	revoked, err := h.revokeAction(caseID, nonconformanceID, "Mira", "action did not address the root cause")
	if err != nil {
		return err
	}
	if err := expectStatus(revoked, http.StatusOK); err != nil {
		return err
	}
	revokedState, err := nestedStringPath(revoked.Body, "nonconformances", 0, "nonconformance", "state")
	if err != nil {
		return err
	}
	if revokedState != "open" {
		return fmt.Errorf("nonconformance state after revoke = %q", revokedState)
	}
	revokedActionState, err := nestedStringPath(revoked.Body, "nonconformances", 0, "corrective_actions", 0, "state")
	if err != nil {
		return err
	}
	if revokedActionState != "revoked" {
		return fmt.Errorf("revoked action state = %q", revokedActionState)
	}
	revokedBy, err := nestedStringPath(revoked.Body, "nonconformances", 0, "corrective_actions", 0, "revoked_by")
	if err != nil {
		return err
	}
	if revokedBy != "Mira" {
		return fmt.Errorf("revoked_by = %q", revokedBy)
	}

	blocked, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "trying to continue after revocation",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(blocked, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(blocked, "disposition_incomplete"); err != nil {
		return err
	}

	// The original failed measurement is unchanged by the disposition churn.
	afterRevoke, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(afterRevoke, http.StatusOK); err != nil {
		return err
	}
	preservedID, err := nestedStringPath(afterRevoke.Body, "measurements", 0, "id")
	if err != nil {
		return err
	}
	if preservedID != failedMeasurementID {
		return fmt.Errorf("failed measurement rewritten: %q != %q", preservedID, failedMeasurementID)
	}
	preservedAccepted, err := nestedBool(afterRevoke.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if preservedAccepted {
		return errors.New("failed measurement was flipped to accepted")
	}

	// A replacement action restarts the loop and can be verified.
	resubmitted, err := h.submitAction(caseID, nonconformanceID, "Rui", "recalibrated the reference standard")
	if err != nil {
		return err
	}
	if err := expectStatus(resubmitted, http.StatusCreated); err != nil {
		return err
	}
	replacementID, err := nestedStringPath(resubmitted.Body, "nonconformances", 0, "corrective_actions", 1, "id")
	if err != nil {
		return err
	}
	if replacementID == actionID {
		return errors.New("resubmission reused the revoked corrective action id")
	}
	actions, err := nestedAny(resubmitted.Body, "nonconformances", 0, "corrective_actions")
	if err != nil {
		return err
	}
	if count := len(actions.([]any)); count != 2 {
		return fmt.Errorf("corrective action history has %d entries, expected 2", count)
	}
	reverified, err := h.verifyAction(caseID, nonconformanceID, "Mira", "recalibration certificate CAL-88")
	if err != nil {
		return err
	}
	if err := expectStatus(reverified, http.StatusOK); err != nil {
		return err
	}

	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "verified disposition authorizes the repeat measurement",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}

	// The follow-up measurement is bound to the disposition loop it closes.
	followUp, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(followUp, http.StatusCreated); err != nil {
		return err
	}
	followUpLink, err := nestedStringPath(followUp.Body, "measurements", 1, "nonconformance_id")
	if err != nil {
		return err
	}
	if followUpLink != nonconformanceID {
		return fmt.Errorf("follow-up measurement links nonconformance %q, expected %q", followUpLink, nonconformanceID)
	}
	readyState, err := nestedString(followUp.Body, "case", "state")
	if err != nil {
		return err
	}
	if readyState != "review_ready" {
		return fmt.Errorf("case state after follow-up = %q", readyState)
	}

	sealed, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Mira",
		"summary":  "disposition loop closed and accepted measurement sealed",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(sealed, http.StatusCreated); err != nil {
		return err
	}

	// A sealed case freezes its disposition record and failed measurement.
	frozenSubmit, err := h.submitAction(caseID, nonconformanceID, "Rui", "late action")
	if err != nil {
		return err
	}
	if err := expectStatus(frozenSubmit, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(frozenSubmit, "state_conflict"); err != nil {
		return err
	}
	frozenRevoke, err := h.revokeAction(caseID, nonconformanceID, "Mira", "late revocation")
	if err != nil {
		return err
	}
	if err := expectStatus(frozenRevoke, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(frozenRevoke, "state_conflict"); err != nil {
		return err
	}

	final, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(final, http.StatusOK); err != nil {
		return err
	}
	finalFailedID, err := nestedStringPath(final.Body, "measurements", 0, "id")
	if err != nil {
		return err
	}
	if finalFailedID != failedMeasurementID {
		return fmt.Errorf("sealed case rewrote the failed measurement: %q", finalFailedID)
	}
	finalFailedAccepted, err := nestedBool(final.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if finalFailedAccepted {
		return errors.New("sealed case flipped the failed measurement to accepted")
	}
	sealID, err := nestedString(final.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing after disposition flow")
	}
	if !hasEvent(final.Body, "corrective_action_revoked") {
		return errors.New("revocation audit event is missing")
	}
	if !hasEvent(final.Body, "corrective_action_verified") {
		return errors.New("verification audit event is missing")
	}
	return nil
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) nonconformanceID(caseID string) (string, error) {
	view, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return "", err
	}
	if err := expectStatus(view, http.StatusOK); err != nil {
		return "", err
	}
	return nestedStringPath(view.Body, "nonconformances", 0, "nonconformance", "id")
}

func (h *harness) submitAction(caseID, nonconformanceID, technician, action string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/nonconformances/"+nonconformanceID+"/actions", map[string]any{
		"technician": technician,
		"action":     action,
	})
}

func (h *harness) verifyAction(caseID, nonconformanceID, reviewer, basis string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/nonconformances/"+nonconformanceID+"/verify", map[string]any{
		"reviewer": reviewer,
		"basis":    basis,
	})
}

func (h *harness) revokeAction(caseID, nonconformanceID, reviewer, reason string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/nonconformances/"+nonconformanceID+"/revoke", map[string]any{
		"reviewer": reviewer,
		"reason":   reason,
	})
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedStringPath(root map[string]any, path ...any) (string, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%v is not a string", path)
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
