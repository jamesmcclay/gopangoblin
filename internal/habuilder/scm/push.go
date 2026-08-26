package scm

import (
	"fmt"
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

// Job is a Strata Cloud Manager configuration job (e.g. a commit-and-push).
type Job struct {
	ID        string `json:"id"`
	StatusStr string `json:"status_str"` // ACT, FIN, PEND, PUSHSENT, PUSHFAIL, PUSHABORT, PUSHTIMEOUT
	ResultStr string `json:"result_str"` // OK, FAIL, PEND, WAIT, CANCELLED, TIMEOUT
	Percent   string `json:"percent"`
	Summary   string `json:"summary"`
	Details   string `json:"details"`
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

// WaitForJob polls a job until it reaches a terminal status or the timeout elapses.
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
