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
		"register-device":                  checkRegisterDevice,
		"open-verification-case":           checkOpenVerificationCase,
		"record-measurement":               checkRecordMeasurement,
		"reopen-verification-case":         checkReopenVerificationCase,
		"nonconformance-corrective-action": checkNonconformanceCorrectiveAction,
		"seal-verification":                checkSealVerification,
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
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
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
	if err := expectErrorCode(blocked, "state_conflict"); err != nil {
		return err
	}

	ncID, err := h.onlyNonconformanceID(caseID)
	if err != nil {
		return err
	}
	if _, err := h.recordAction(caseID, ncID, "Rui", "replaced the fixture and re-zeroed the instrument", "setup sheet SOP-7"); err != nil {
		return err
	}
	dispositioned, err := h.disposition(caseID, ncID, "Mira", "fixture replacement verified against SOP-7")
	if err != nil {
		return err
	}
	if err := expectStatus(dispositioned, http.StatusOK); err != nil {
		return err
	}

	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "disposition complete, re-measurement authorized",
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
	if !hasEvent(events.Body, "nonconformance_dispositioned") {
		return errors.New("disposition audit event is missing")
	}
	return nil
}

func checkNonconformanceCorrectiveAction() error {
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
	ncID, err := h.onlyNonconformanceID(caseID)
	if err != nil {
		return err
	}
	failedID, err := nestedAnyString(view.Body, "measurements", 0, "id")
	if err != nil {
		return err
	}
	linkedID, err := nestedAnyString(view.Body, "nonconformances", 0, "nonconformance", "measurement_id")
	if err != nil {
		return err
	}
	if linkedID != failedID {
		return fmt.Errorf("nonconformance references measurement %q, expected failed measurement %q", linkedID, failedID)
	}
	ncStatus, err := nestedAnyString(view.Body, "nonconformances", 0, "nonconformance", "status")
	if err != nil {
		return err
	}
	if ncStatus != "open" {
		return fmt.Errorf("nonconformance status = %q, expected open", ncStatus)
	}

	missingRationale, err := h.request(http.MethodPost, "/cases/"+caseID+"/nonconformances/"+ncID+"/actions", map[string]any{
		"actor":  "Rui",
		"action": "recalibrated the probe",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(missingRationale, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(missingRationale, "invalid_input"); err != nil {
		return err
	}

	recorded, err := h.recordAction(caseID, ncID, "Rui", "recalibrated the probe", "calibration certificate CAL-44")
	if err != nil {
		return err
	}
	actionID, err := nestedAnyString(recorded.Body, "nonconformances", 0, "actions", 0, "id")
	if err != nil {
		return err
	}
	actionStatus, err := nestedAnyString(recorded.Body, "nonconformances", 0, "actions", 0, "status")
	if err != nil {
		return err
	}
	if actionStatus != "active" {
		return fmt.Errorf("corrective action status = %q, expected active", actionStatus)
	}

	revoked, err := h.revoke(caseID, ncID, actionID, "Mira", "action named the wrong instrument")
	if err != nil {
		return err
	}
	if err := expectStatus(revoked, http.StatusOK); err != nil {
		return err
	}
	revokedStatus, err := nestedAnyString(revoked.Body, "nonconformances", 0, "actions", 0, "status")
	if err != nil {
		return err
	}
	if revokedStatus != "revoked" {
		return fmt.Errorf("revoked action status = %q", revokedStatus)
	}
	keptAction, err := nestedAnyString(revoked.Body, "nonconformances", 0, "actions", 0, "action")
	if err != nil {
		return err
	}
	if keptAction != "recalibrated the probe" {
		return fmt.Errorf("revocation rewrote the original action: %q", keptAction)
	}

	duplicateRevoke, err := h.revoke(caseID, ncID, actionID, "Mira", "duplicate revoke")
	if err != nil {
		return err
	}
	if err := expectStatus(duplicateRevoke, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(duplicateRevoke, "state_conflict"); err != nil {
		return err
	}

	noActive, err := h.disposition(caseID, ncID, "Mira", "all actions revoked")
	if err != nil {
		return err
	}
	if err := expectStatus(noActive, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(noActive, "state_conflict"); err != nil {
		return err
	}

	if _, err := h.recordAction(caseID, ncID, "Rui", "replaced the fixture and re-zeroed the instrument", "setup sheet SOP-7"); err != nil {
		return err
	}

	emptyBasis, err := h.disposition(caseID, ncID, "Mira", "  ")
	if err != nil {
		return err
	}
	if err := expectStatus(emptyBasis, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(emptyBasis, "invalid_input"); err != nil {
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
			response, err := h.disposition(caseID, ncID, fmt.Sprintf("Reviewer-%d", index+1), "disposition basis per SOP-7")
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
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent disposition status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent disposition outcomes: %d success, %d conflict", successes, conflicts)
	}

	duplicateDisposition, err := h.disposition(caseID, ncID, "Mira", "duplicate disposition")
	if err != nil {
		return err
	}
	if err := expectStatus(duplicateDisposition, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(duplicateDisposition, "state_conflict"); err != nil {
		return err
	}

	lateRevoke, err := h.revoke(caseID, ncID, actionID, "Mira", "revoke after disposition")
	if err != nil {
		return err
	}
	if err := expectStatus(lateRevoke, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(lateRevoke, "state_conflict"); err != nil {
		return err
	}

	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "disposition complete, re-measurement authorized",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}

	passed, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(passed, http.StatusCreated); err != nil {
		return err
	}
	link, err := nestedAnyString(passed.Body, "measurements", 1, "nonconformance_id")
	if err != nil {
		return err
	}
	if link != ncID {
		return fmt.Errorf("re-measurement linked to nonconformance %q, expected %q", link, ncID)
	}
	passedState, err := nestedString(passed.Body, "case", "state")
	if err != nil {
		return err
	}
	if passedState != "review_ready" {
		return fmt.Errorf("case state after re-measurement = %q", passedState)
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

	final, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(final, http.StatusOK); err != nil {
		return err
	}
	failedAccepted, err := nestedBool(final.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if failedAccepted {
		return errors.New("original failed measurement was rewritten as accepted")
	}
	deviation, err := nestedNumber(final.Body, "measurements", 0, "deviation")
	if err != nil {
		return err
	}
	if deviation != 1.5 {
		return fmt.Errorf("original failed measurement deviation = %v, expected 1.5", deviation)
	}
	firstMeasurement, err := nestedAny(final.Body, "measurements", 0)
	if err != nil {
		return err
	}
	if _, ok := firstMeasurement.(map[string]any)["nonconformance_id"]; ok {
		return errors.New("original failed measurement gained a nonconformance link")
	}
	if _, err := nestedString(final.Body, "seal", "id"); err != nil {
		return err
	}

	list, err := h.request(http.MethodGet, "/cases/"+caseID+"/nonconformances", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(list, http.StatusOK); err != nil {
		return err
	}
	entries, err := nestedSlice(list.Body, "nonconformances")
	if err != nil {
		return err
	}
	if len(entries) != 1 {
		return fmt.Errorf("nonconformance list has %d entries, expected 1", len(entries))
	}
	single, err := h.request(http.MethodGet, "/cases/"+caseID+"/nonconformances/"+ncID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(single, http.StatusOK); err != nil {
		return err
	}
	singleStatus, err := nestedAnyString(single.Body, "nonconformance", "status")
	if err != nil {
		return err
	}
	if singleStatus != "dispositioned" {
		return fmt.Errorf("disposition record status = %q", singleStatus)
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	for _, kind := range []string{"nonconformance_opened", "corrective_action_recorded", "corrective_action_revoked", "nonconformance_dispositioned"} {
		if !hasEvent(events.Body, kind) {
			return fmt.Errorf("audit event %s is missing", kind)
		}
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

func (h *harness) onlyNonconformanceID(caseID string) (string, error) {
	view, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return "", err
	}
	if err := expectStatus(view, http.StatusOK); err != nil {
		return "", err
	}
	entries, err := nestedSlice(view.Body, "nonconformances")
	if err != nil {
		return "", err
	}
	if len(entries) != 1 {
		return "", fmt.Errorf("case %s has %d nonconformances, expected 1", caseID, len(entries))
	}
	return nestedAnyString(view.Body, "nonconformances", 0, "nonconformance", "id")
}

func (h *harness) recordAction(caseID, nonconformanceID, actor, action, rationale string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases/"+caseID+"/nonconformances/"+nonconformanceID+"/actions", map[string]any{
		"actor":     actor,
		"action":    action,
		"rationale": rationale,
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) disposition(caseID, nonconformanceID, reviewer, basis string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/nonconformances/"+nonconformanceID+"/disposition", map[string]any{
		"reviewer": reviewer,
		"basis":    basis,
	})
}

func (h *harness) revoke(caseID, nonconformanceID, actionID, actor, reason string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/nonconformances/"+nonconformanceID+"/actions/"+actionID+"/revoke", map[string]any{
		"actor":  actor,
		"reason": reason,
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

func nestedNumber(root map[string]any, path ...any) (float64, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%v is not a number", path)
	}
	return number, nil
}

func nestedAnyString(root map[string]any, path ...any) (string, error) {
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
