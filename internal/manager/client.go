package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Volume represents a DRBD volume in bloc-manager.
type Volume struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Minor      int      `json:"minor"`
	SizeMB     int      `json:"size_mb"`
	AttachedTo string   `json:"attached_to"`
	Nodes      []string `json:"nodes"`
}

// PublishResult is returned by PublishVolume.
type PublishResult struct {
	Node   string `json:"node"`
	Device string `json:"device"`
}

// Client is an HTTP client for the bloc-manager REST API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a new manager Client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &NotFoundError{Path: path}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %s %s: status %d: %s", method, path, resp.StatusCode, string(b))
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// NotFoundError is returned when the manager responds with 404.
type NotFoundError struct {
	Path string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("not found: %s", e.Path)
}

// IsNotFound returns true if the error is a NotFoundError.
func IsNotFound(err error) bool {
	_, ok := err.(*NotFoundError)
	return ok
}

// CreateVolume creates a new volume in bloc-manager.
func (c *Client) CreateVolume(ctx context.Context, name string, sizeMB int, nodes []string) (*Volume, error) {
	body := map[string]interface{}{
		"name":    name,
		"size_mb": sizeMB,
		"nodes":   nodes,
	}
	var v Volume
	if err := c.do(ctx, http.MethodPost, "/volumes", body, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// DeleteVolume deletes a volume by ID.
func (c *Client) DeleteVolume(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/volumes/"+id, nil, nil)
}

// GetVolume retrieves a volume by ID.
func (c *Client) GetVolume(ctx context.Context, id string) (*Volume, error) {
	var v Volume
	if err := c.do(ctx, http.MethodGet, "/volumes/"+id, nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// PublishVolume attaches a volume to the given node and returns device info.
func (c *Client) PublishVolume(ctx context.Context, id, node string) (*PublishResult, error) {
	body := map[string]string{"node": node}
	var result PublishResult
	if err := c.do(ctx, http.MethodPost, "/volumes/"+id+"/publish", body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UnpublishVolume detaches a volume from its current node.
func (c *Client) UnpublishVolume(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/volumes/"+id+"/publish", nil, nil)
}

// ResizeVolume resizes a volume to newSizeMB.
func (c *Client) ResizeVolume(ctx context.Context, id string, newSizeMB int) error {
	body := map[string]int{"new_size_mb": newSizeMB}
	return c.do(ctx, http.MethodPost, "/volumes/"+id+"/resize", body, nil)
}
