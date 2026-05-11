package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// CommandRequest is the body for POST /v1/commands.
type CommandRequest struct {
	InstanceID             string `json:"instance_id"`
	AccountID              string `json:"account_id"`
	Command                string `json:"command"`
	JiraTicketID           string `json:"jira_ticket_id,omitempty"`
	EffectiveForValidation string `json:"effective_for_validation,omitempty"`
	ClientVersion          string `json:"client_version,omitempty"`
	PrivateIP              string `json:"private_ip,omitempty"`
}

// CommandResponse is the response from POST /v1/commands.
type CommandResponse struct {
	RequestID        string `json:"request_id"`
	Status           string `json:"status"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Stdout           string `json:"stdout,omitempty"`
	Stderr           string `json:"stderr,omitempty"`
	Truncated        bool   `json:"truncated"`
	DurationMs       int64  `json:"duration_ms"`
	EffectiveCommand string `json:"effective_command,omitempty"`
	JiraTicketID     string `json:"jira_ticket_id,omitempty"`
	Reason           string `json:"reason,omitempty"`
	TicketKey        string `json:"ticket_key,omitempty"`
	TicketURL        string `json:"ticket_url,omitempty"`
	Message          string `json:"message,omitempty"`
}

// UploadRequest is the body for POST /v1/upload.
type UploadRequest struct {
	InstanceID string `json:"instance_id"`
	AccountID  string `json:"account_id"`
	DestPath   string `json:"dest_path"`
	Filename   string `json:"filename"`
	ContentB64 string `json:"content_b64"`
	SHA256     string `json:"sha256"`
	TicketID   string `json:"ticket_id,omitempty"`
}

// DownloadRequest is the body for POST /v1/download.
type DownloadRequest struct {
	InstanceID string `json:"instance_id"`
	AccountID  string `json:"account_id"`
	SrcPath    string `json:"src_path"`
	TicketID   string `json:"ticket_id,omitempty"`
}

// DownloadResponse is the response from POST /v1/download.
type DownloadResponse struct {
	RequestID    string `json:"request_id"`
	Status       string `json:"status"`
	PresignedURL string `json:"presigned_url,omitempty"`
	Filename     string `json:"filename,omitempty"`
	TicketKey    string `json:"ticket_key,omitempty"`
	TicketURL    string `json:"ticket_url,omitempty"`
	Message      string `json:"message,omitempty"`
}

// InstanceInfo describes a single EC2 instance returned by /v1/instances.
type InstanceInfo struct {
	Name         string `json:"name"`
	InstanceID   string `json:"instance_id"`
	PrivateIP    string `json:"private_ip"`
	PublicIP     string `json:"public_ip"`
	InstanceType string `json:"instance_type"`
	State        string `json:"state"`
	AZ           string `json:"az"`
}

// ListInstancesResponse is the response from POST /v1/instances.
type ListInstancesResponse struct {
	Instances []InstanceInfo `json:"instances"`
}

// ListInstances calls POST /v1/instances for the given account.
func (c *Client) ListInstances(ctx context.Context, accountID string) ([]InstanceInfo, error) {
	var resp ListInstancesResponse
	if err := c.postJSON(ctx, "/v1/instances",
		map[string]string{"account_id": accountID}, &resp); err != nil {
		return nil, err
	}
	return resp.Instances, nil
}

// Upload calls POST /v1/upload.
func (c *Client) Upload(ctx context.Context, req UploadRequest) (*CommandResponse, error) {
	var resp CommandResponse
	if err := c.postJSON(ctx, "/v1/upload", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// StageRequest is the body for POST /v1/stage.
type StageRequest struct {
	Filename   string `json:"filename"`
	ContentB64 string `json:"content_b64"`
	SHA256     string `json:"sha256"`
}

// StageResponse is the response from POST /v1/stage.
type StageResponse struct {
	PresignedURL string `json:"presigned_url"`
	S3Key        string `json:"s3_key"`
	ExpiresInSec int    `json:"expires_in_seconds"`
}

// StageFile calls POST /v1/stage.
func (c *Client) StageFile(ctx context.Context, req StageRequest) (*StageResponse, error) {
	var resp StageResponse
	if err := c.postJSON(ctx, "/v1/stage", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AttachRequest is the body for POST /v1/attach.
type AttachRequest struct {
	TicketKey  string `json:"ticket_key"`
	Filename   string `json:"filename"`
	ContentB64 string `json:"content_b64"`
}

// AttachToTicket calls POST /v1/attach.
func (c *Client) AttachToTicket(ctx context.Context, req AttachRequest) error {
	var resp map[string]string
	return c.postJSON(ctx, "/v1/attach", req, &resp)
}

// Download calls POST /v1/download.
func (c *Client) Download(ctx context.Context, req DownloadRequest) (*DownloadResponse, error) {
	var resp DownloadResponse
	if err := c.postJSON(ctx, "/v1/download", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CompleteRequest is the body for POST /v1/complete.
type CompleteRequest struct {
	InstanceID string `json:"instance_id"`
	AccountID  string `json:"account_id"`
	Partial    string `json:"partial"`
	CurrentDir string `json:"current_dir"`
	PrivateIP  string `json:"private_ip,omitempty"`
}

// CompleteResponse is the response from POST /v1/complete.
type CompleteResponse struct {
	Completions []string `json:"completions"`
}

// Complete calls POST /v1/complete.
func (c *Client) Complete(ctx context.Context, req CompleteRequest) ([]string, error) {
	var resp CompleteResponse
	if err := c.postJSON(ctx, "/v1/complete", req, &resp); err != nil {
		return nil, err
	}
	return resp.Completions, nil
}

// ClassifyResponse is the response from POST /v1/classify.
type ClassifyResponse struct {
	Tier        string   `json:"tier"`
	Reason      string   `json:"reason,omitempty"`
	RewrittenTo []string `json:"rewritten_to,omitempty"`
}

// AccountInfo describes a single AWS account returned by /v1/accounts.
type AccountInfo struct {
	Label     string `json:"label"`
	AccountID string `json:"account_id"`
}

// AccountsResponse is the response from GET /v1/accounts.
type AccountsResponse struct {
	Accounts []AccountInfo `json:"accounts"`
}

// ListAccounts calls GET /v1/accounts.
func (c *Client) ListAccounts(ctx context.Context) ([]AccountInfo, error) {
	var resp AccountsResponse
	if err := c.getJSON(ctx, "/v1/accounts", &resp); err != nil {
		return nil, err
	}
	return resp.Accounts, nil
}

// APIError represents a non-2xx response from the backend.
type APIError struct {
	HTTPStatus int
	Message    string
	Reason     string
}

func (e *APIError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("backend error %d: %s", e.HTTPStatus, e.Reason)
	}
	return fmt.Sprintf("backend error %d: %s", e.HTTPStatus, e.Message)
}

// Client is the bbctl-backend HTTP client.
type Client struct {
	baseURL       string
	token         string
	clientVersion string
	http          *http.Client
}

// New creates a new Client.
func New(baseURL, token, clientVersion string) *Client {
	return &Client{
		baseURL:       baseURL,
		token:         token,
		clientVersion: clientVersion,
		http:          &http.Client{},
	}
}

// RunCommand calls POST /v1/commands.
func (c *Client) RunCommand(ctx context.Context, req CommandRequest) (*CommandResponse, error) {
	req.ClientVersion = c.clientVersion
	var resp CommandResponse
	if err := c.postJSON(ctx, "/v1/commands", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Classify calls POST /v1/classify.
func (c *Client) Classify(ctx context.Context, command, instanceID string) (*ClassifyResponse, error) {
	body := map[string]string{"command": command, "instance_id": instanceID}
	var resp ClassifyResponse
	if err := c.postJSON(ctx, "/v1/classify", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CancelCommand calls DELETE /v1/commands/{requestID}.
func (c *Client) CancelCommand(ctx context.Context, requestID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/v1/commands/"+requestID, nil)
	if err != nil {
		return err
	}
	c.addAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.addAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.parseError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) postJSON(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.parseError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) addAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *Client) parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var e struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &e)
	return &APIError{
		HTTPStatus: resp.StatusCode,
		Message:    e.Error,
		Reason:     e.Reason,
	}
}
