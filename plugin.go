package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"

	pluginv1 "github.com/robwittman/pillar/gen/proto/pillar/plugin/v1"
)

const (
	// Gitea enforces a max username length of 40 characters.
	maxUsernameLen = 40
	prefix         = "pillar-agent-"
)

type giteaPlugin struct {
	client *Client
	// Optional team memberships (org/team format) to assign to every created user.
	teams []string
	// Whether to purge user data (repos, issues, comments) on deletion.
	purgeOnDelete bool
	// Email domain used for generated agent email addresses.
	emailDomain string
}

func (p *giteaPlugin) Configure(config map[string]string) error {
	baseURL := config["base_url"]
	adminToken := config["admin_token"]

	if baseURL == "" || adminToken == "" {
		return fmt.Errorf("base_url and admin_token are required")
	}

	p.client = NewClient(baseURL, adminToken)

	if v := config["teams"]; v != "" {
		p.teams = splitCSV(v)
	}
	p.purgeOnDelete = config["purge_on_delete"] == "true"

	if v := config["email_domain"]; v != "" {
		p.emailDomain = v
	} else {
		// Derive from base_url hostname (e.g. "https://git.example.com" -> "git.example.com").
		p.emailDomain = extractHost(baseURL)
	}

	log.Printf("gitea plugin configured: base_url=%s teams=%v purge_on_delete=%v email_domain=%s", baseURL, p.teams, p.purgeOnDelete, p.emailDomain)
	return nil
}

func (p *giteaPlugin) OnEvent(event *pluginv1.EventRequest) (*pluginv1.EventResponse, error) {
	switch event.Type {
	case "agent.created":
		return p.handleAgentCreated(event)
	case "agent.deleted":
		return p.handleAgentDeleted(event)
	default:
		return &pluginv1.EventResponse{Success: true}, nil
	}
}

func (p *giteaPlugin) handleAgentCreated(event *pluginv1.EventRequest) (*pluginv1.EventResponse, error) {
	var agent struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(event.Data, &agent); err != nil {
		return &pluginv1.EventResponse{
			Success: false,
			Error:   fmt.Sprintf("parse agent data: %v", err),
		}, nil
	}

	// Truncate the agent ID if needed to stay within Gitea's username limit.
	agentID := agent.ID
	if len(prefix)+len(agentID) > maxUsernameLen {
		agentID = agentID[:maxUsernameLen-len(prefix)]
	}
	username := fmt.Sprintf("%s%s", prefix, agentID)
	email := fmt.Sprintf("%s%s@%s", prefix, agentID, p.emailDomain)

	// Create the Gitea user.
	user, password, err := p.client.CreateUser(username, email, agent.Name)
	if err != nil {
		return &pluginv1.EventResponse{
			Success: false,
			Error:   fmt.Sprintf("create user: %v", err),
		}, nil
	}
	log.Printf("created gitea user %s for agent %s", username, agent.ID)

	// Add to configured teams (users join orgs through team membership).
	for _, team := range p.teams {
		parts := strings.SplitN(team, "/", 2)
		if len(parts) != 2 {
			log.Printf("warning: team %q should be in org/team format, skipping", team)
			continue
		}
		if err := p.client.AddTeamMember(parts[0], parts[1], username); err != nil {
			log.Printf("warning: add user %s to team %s: %v", username, team, err)
		}
	}

	// Generate an API token for the agent.
	token, err := p.client.CreateToken(username, password, "pillar")
	if err != nil {
		return &pluginv1.EventResponse{
			Success: false,
			Error:   fmt.Sprintf("create token: %v", err),
		}, nil
	}
	log.Printf("created gitea API token for agent %s", agent.ID)

	creds := GiteaCredentials{
		BaseURL:  p.client.baseURL,
		Username: username,
		UserID:   user.ID,
		Token:    token,
	}
	value, _ := json.Marshal(creds)

	return &pluginv1.EventResponse{
		Success: true,
		Attributes: []*pluginv1.AttributeWrite{
			{
				AgentId:   agent.ID,
				Namespace: "gitea",
				Value:     value,
			},
		},
	}, nil
}

func (p *giteaPlugin) handleAgentDeleted(event *pluginv1.EventRequest) (*pluginv1.EventResponse, error) {
	var agent struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(event.Data, &agent); err != nil {
		return &pluginv1.EventResponse{
			Success: false,
			Error:   fmt.Sprintf("parse agent data: %v", err),
		}, nil
	}

	agentID := agent.ID
	if len(prefix)+len(agentID) > maxUsernameLen {
		agentID = agentID[:maxUsernameLen-len(prefix)]
	}
	username := fmt.Sprintf("%s%s", prefix, agentID)

	if err := p.client.DeleteUser(username, p.purgeOnDelete); err != nil {
		return &pluginv1.EventResponse{
			Success: false,
			Error:   fmt.Sprintf("delete user: %v", err),
		}, nil
	}
	log.Printf("deleted gitea user %s for agent %s", username, agent.ID)

	return &pluginv1.EventResponse{Success: true}, nil
}

func extractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "noreply.localhost"
}

func splitCSV(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		if v := strings.TrimSpace(part); v != "" {
			result = append(result, v)
		}
	}
	return result
}
