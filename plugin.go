package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	pluginv1 "github.com/robwittman/pillar/gen/proto/pillar/plugin/v1"
)

type giteaPlugin struct {
	client *Client
	// Optional org/team memberships to assign to every created user.
	orgs  []string
	teams []string
	// Whether to purge user data (repos, issues, comments) on deletion.
	purgeOnDelete bool
}

func (p *giteaPlugin) Configure(config map[string]string) error {
	baseURL := config["base_url"]
	adminToken := config["admin_token"]

	if baseURL == "" || adminToken == "" {
		return fmt.Errorf("base_url and admin_token are required")
	}

	p.client = NewClient(baseURL, adminToken)

	if v := config["orgs"]; v != "" {
		p.orgs = splitCSV(v)
	}
	if v := config["teams"]; v != "" {
		p.teams = splitCSV(v)
	}
	p.purgeOnDelete = config["purge_on_delete"] == "true"

	log.Printf("gitea plugin configured: base_url=%s orgs=%v teams=%v purge_on_delete=%v", baseURL, p.orgs, p.teams, p.purgeOnDelete)
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

	// Gitea enforces a max username length of 40 characters.
	// Truncate the agent ID if needed to stay within the limit.
	agentID := agent.ID
	const maxUsernameLen = 40
	const prefix = "pillar-agent-"
	if len(prefix)+len(agentID) > maxUsernameLen {
		agentID = agentID[:maxUsernameLen-len(prefix)]
	}
	username := fmt.Sprintf("%s%s", prefix, agentID)
	email := fmt.Sprintf("%s%s@localhost", prefix, agentID)

	// Create the Gitea user.
	user, err := p.client.CreateUser(username, email, agent.Name)
	if err != nil {
		return &pluginv1.EventResponse{
			Success: false,
			Error:   fmt.Sprintf("create user: %v", err),
		}, nil
	}
	log.Printf("created gitea user %s for agent %s", username, agent.ID)

	// Add to configured organizations.
	for _, org := range p.orgs {
		if err := p.client.AddOrgMember(org, username); err != nil {
			log.Printf("warning: add user %s to org %s: %v", username, org, err)
		}
	}

	// Add to configured teams (by name, looked up within orgs).
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
	token, err := p.client.CreateToken(username, "pillar")
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

	username := fmt.Sprintf("pillar-agent-%s", agent.ID)

	if err := p.client.DeleteUser(username, p.purgeOnDelete); err != nil {
		return &pluginv1.EventResponse{
			Success: false,
			Error:   fmt.Sprintf("delete user: %v", err),
		}, nil
	}
	log.Printf("deleted gitea user %s for agent %s", username, agent.ID)

	return &pluginv1.EventResponse{Success: true}, nil
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
