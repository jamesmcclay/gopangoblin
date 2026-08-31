package scm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// PushResult is the response from triggering a candidate config push.
type PushResult struct {
	Success bool   `json:"success"`
	JobID   string `json:"job_id"`
	Message string `json:"message"`
}

// PushCandidateConfig pushes the candidate configuration for the given
// device serial numbers, returning the SCM job tracking the push.
// See https://pan.dev/scm/api/config/ngfw/operations/operations-api-ngfw/
func (c *Client) PushCandidateConfig(deviceSerials []string, description string) (*PushResult, error) {
	body := struct {
		Devices     []string `json:"devices"`
		Description string   `json:"description,omitempty"`
	}{
		Devices:     deviceSerials,
		Description: description,
	}

	var out PushResult
	if err := c.doJSON("POST", "/config/operations/v1/config-versions/candidate:push", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Job is a Strata Cloud Manager configuration job (e.g. a commit-and-push,
// or one device's push within it).
//
// A "candidate:push" call returns the ID of a top-level "CommitAndPush"
// job, but that job's own status only reflects SCM's internal candidate
// commit -- it reaches FIN/OK even when the actual per-device push fails.
// The real per-device outcome lives in separate "CommitAll" child jobs
// (ParentID set to the CommitAndPush job's ID), confirmed live: a push
// this tool reported as fully successful had its child job fail with
// PUSHABORT/PUSHFAIL and a real PAN-OS error, silently, for every push in
// a full testing session -- so any code waiting on a push MUST wait for
// and check these child jobs too, not just the top-level job returned by
// PushCandidateConfig. See WaitForPush.
type Job struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id"`
	DeviceName string `json:"device_name"`
	StatusStr  string `json:"status_str"` // ACT, FIN, PEND, XFMPEND, PUSHSENT, PUSHFAIL, PUSHABORT, PUSHTIMEOUT
	ResultStr  string `json:"result_str"` // OK, FAIL, PEND, WAIT, CANCELLED, TIMEOUT
	Percent    string `json:"percent"`
	Summary    string `json:"summary"`
	Details    string `json:"details"`
}

// Done reports whether the job has reached a terminal status.
func (j Job) Done() bool {
	switch j.StatusStr {
	case "FIN", "PUSHFAIL", "PUSHABORT", "PUSHTIMEOUT":
		return true
	default:
		return false
	}
}

// JobDetails is the parsed shape of a Job's Details field, which is
// itself a JSON-encoded string (not a nested object) whenever present.
type JobDetails struct {
	Info     []string `json:"info"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// ParseDetails parses j.Details, returning nil (no error) if it's empty.
func (j Job) ParseDetails() (*JobDetails, error) {
	if j.Details == "" {
		return nil, nil
	}
	var d JobDetails
	if err := json.Unmarshal([]byte(j.Details), &d); err != nil {
		return nil, fmt.Errorf("parsing job %s details: %w", j.ID, err)
	}
	return &d, nil
}

// GetJob retrieves a configuration job by ID.
func (c *Client) GetJob(id string) (*Job, error) {
	var resp struct {
		Data []Job `json:"data"`
	}
	if err := c.doJSON("GET", "/config/operations/v1/jobs/"+id, nil, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("job %s: not found in response", id)
	}
	return &resp.Data[0], nil
}

// ListJobs returns the most recent jobs (newest first), up to limit.
// There's no server-side filter for a job's children (parent_id isn't an
// accepted query param, confirmed live), so finding a job's children
// means listing recent jobs and filtering client-side by ParentID.
func (c *Client) ListJobs(limit int) ([]Job, error) {
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))

	var resp struct {
		Data []Job `json:"data"`
	}
	if err := c.doJSON("GET", "/config/operations/v1/jobs", q, nil, &resp); err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	return resp.Data, nil
}

// WaitForJob polls a single job until it reaches a terminal status or the
// timeout elapses. For a push job specifically, prefer WaitForPush: this
// alone only reflects SCM's internal candidate commit, not whether the
// config actually reached and applied on any device.
func (c *Client) WaitForJob(id string, timeout, pollInterval time.Duration) (*Job, error) {
	deadline := time.Now().Add(timeout)
	for {
		job, err := c.GetJob(id)
		if err != nil {
			return nil, err
		}
		if job.Done() {
			return job, nil
		}
		if time.Now().After(deadline) {
			return job, fmt.Errorf("job %s did not finish within %s (last status %s)", id, timeout, job.StatusStr)
		}
		time.Sleep(pollInterval)
	}
}

// PushOutcome is the full result of a candidate config push: the
// top-level commit job, plus every per-device push job it spawned.
type PushOutcome struct {
	CommitJob  Job
	DeviceJobs []Job
}

// Failed reports whether the commit itself or any per-device push failed.
func (o PushOutcome) Failed() bool {
	if o.CommitJob.ResultStr != "OK" {
		return true
	}
	for _, j := range o.DeviceJobs {
		if j.ResultStr != "OK" {
			return true
		}
	}
	return false
}

// Summary returns a human-readable multi-line description of any
// failures, using each failed job's parsed Details when available.
func (o PushOutcome) Summary() string {
	var lines []string
	if o.CommitJob.ResultStr != "OK" {
		lines = append(lines, fmt.Sprintf("commit job %s: %s (%s)", o.CommitJob.ID, o.CommitJob.ResultStr, o.CommitJob.StatusStr))
	}
	for _, j := range o.DeviceJobs {
		if j.ResultStr == "OK" {
			continue
		}
		line := fmt.Sprintf("device %s: push %s (%s)", j.DeviceName, j.ResultStr, j.StatusStr)
		if d, err := j.ParseDetails(); err == nil && d != nil && len(d.Errors) > 0 {
			line += ": " + fmt.Sprint(d.Errors)
		}
		lines = append(lines, line)
	}
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "; "
		}
		out += l
	}
	return out
}

// WaitForPush waits for a candidate:push job to fully resolve: both the
// top-level CommitAndPush job AND every per-device CommitAll child job it
// spawns (see the Job doc comment for why the top-level job alone isn't
// enough). expectedDevices is the number of devices the push targeted,
// used to know how many child jobs to wait for -- children can still be
// appearing in the job list for a short time after the parent job itself
// reaches FIN.
func (c *Client) WaitForPush(jobID string, expectedDevices int, timeout, pollInterval time.Duration) (*PushOutcome, error) {
	deadline := time.Now().Add(timeout)

	commitJob, err := c.waitForJobDeadline(jobID, deadline, pollInterval)
	if err != nil {
		return nil, err
	}

	var children map[string]Job
	for {
		jobs, err := c.ListJobs(200)
		if err != nil {
			return nil, err
		}
		children = map[string]Job{}
		allDone := true
		for _, j := range jobs {
			if j.ParentID != jobID {
				continue
			}
			children[j.ID] = j
			if !j.Done() {
				allDone = false
			}
		}
		if len(children) >= expectedDevices && allDone {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("push job %s: device push job(s) did not finish within %s (found %d of %d expected)", jobID, timeout, len(children), expectedDevices)
		}
		time.Sleep(pollInterval)
	}

	outcome := &PushOutcome{CommitJob: *commitJob}
	for id := range children {
		full, err := c.GetJob(id)
		if err != nil {
			return nil, err
		}
		outcome.DeviceJobs = append(outcome.DeviceJobs, *full)
	}
	return outcome, nil
}

func (c *Client) waitForJobDeadline(id string, deadline time.Time, pollInterval time.Duration) (*Job, error) {
	for {
		job, err := c.GetJob(id)
		if err != nil {
			return nil, err
		}
		if job.Done() {
			return job, nil
		}
		if time.Now().After(deadline) {
			return job, fmt.Errorf("job %s did not finish within deadline (last status %s)", id, job.StatusStr)
		}
		time.Sleep(pollInterval)
	}
}
