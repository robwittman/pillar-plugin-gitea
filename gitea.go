package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client wraps the Gitea Admin API.
type Client struct {
	baseURL    string
	adminToken string
	httpClient *http.Client
}

// NewClient creates a Gitea API client authenticated with an admin token.
func NewClient(baseURL, adminToken string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		adminToken: adminToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GiteaCredentials holds the credentials returned to the agent.
type GiteaCredentials struct {
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	UserID   int64  `json:"user_id"`
	Token    string `json:"token"`
}

// User represents a Gitea user (subset of fields we care about).
type User struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
}

// CreateUser creates a new Gitea user via the admin API.
func (c *Client) CreateUser(username, email, fullName string) (*User, error) {
	payload := map[string]any{
		"username":             username,
		"email":                email,
		"full_name":            fullName,
		"password":             generatePassword(),
		"must_change_password": false,
		"visibility":           "private",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	resp, err := c.do(http.MethodPost, "/api/v1/admin/users", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create user: status %d: %s", resp.StatusCode, respBody)
	}

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode user: %w", err)
	}
	return &user, nil
}

// DeleteUser removes a Gitea user via the admin API.
// If purge is true, all the user's repos, issues, and comments are also removed.
func (c *Client) DeleteUser(username string, purge bool) error {
	path := fmt.Sprintf("/api/v1/admin/users/%s", username)
	if purge {
		path += "?purge=true"
	}
	resp, err := c.do(http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete user: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// AddOrgMember adds a user to a Gitea organization.
func (c *Client) AddOrgMember(org, username string) error {
	resp, err := c.do(http.MethodPut, fmt.Sprintf("/api/v1/orgs/%s/members/%s", org, username), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add org member: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// AddTeamMember adds a user to a team within an organization.
// The team is looked up by name within the given org.
func (c *Client) AddTeamMember(org, teamName, username string) error {
	teamID, err := c.findTeamID(org, teamName)
	if err != nil {
		return fmt.Errorf("find team: %w", err)
	}

	resp, err := c.do(http.MethodPut, fmt.Sprintf("/api/v1/teams/%d/members/%s", teamID, username), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add team member: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// CreateToken creates an API token for a user via the admin-like user tokens endpoint.
func (c *Client) CreateToken(username, tokenName string) (string, error) {
	payload := map[string]any{
		"name":   tokenName,
		"scopes": []string{"all"},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	resp, err := c.do(http.MethodPost, fmt.Sprintf("/api/v1/users/%s/tokens", username), body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create token: status %d: %s", resp.StatusCode, respBody)
	}

	var tokenResp struct {
		SHA1 string `json:"sha1"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode token: %w", err)
	}
	return tokenResp.SHA1, nil
}

// findTeamID looks up a team's numeric ID by org and team name.
func (c *Client) findTeamID(org, teamName string) (int64, error) {
	resp, err := c.do(http.MethodGet, fmt.Sprintf("/api/v1/orgs/%s/teams", org), nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("list teams: status %d: %s", resp.StatusCode, respBody)
	}

	var teams []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&teams); err != nil {
		return 0, fmt.Errorf("decode teams: %w", err)
	}

	for _, t := range teams {
		if strings.EqualFold(t.Name, teamName) {
			return t.ID, nil
		}
	}
	return 0, fmt.Errorf("team %q not found in org %q", teamName, org)
}

// do executes an authenticated HTTP request against the Gitea API.
func (c *Client) do(method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.adminToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

// generatePassword creates a random password for the Gitea user.
// The agent won't use it directly (it uses the API token), but Gitea requires one.
func generatePassword() string {
	// Use crypto/rand for a secure random password.
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%"
	b := make([]byte, 32)
	// Read from crypto/rand
	cryptoRandRead(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}
