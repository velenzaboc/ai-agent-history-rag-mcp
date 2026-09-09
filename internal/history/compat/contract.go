// Package compat validates the native production compatibility contract.
package compat

import (
	"encoding/json"
	"fmt"
	"strings"
)

const CurrentVersion = "1.0.0"

type Contract struct {
	Version       string         `json:"version"`
	PayloadLimits []PayloadLimit `json:"payload_limits"`
	MCPTools      []MCPTool      `json:"mcp_tools"`
	HTTPEndpoints []HTTPEndpoint `json:"http_endpoints"`
	Sources       []SourceFamily `json:"sources"`
	Environment   []Environment  `json:"environment"`
	Entrypoints   []Entrypoint   `json:"entrypoints"`
}

type PayloadLimit struct {
	Name      string `json:"name"`
	MaxBytes  int64  `json:"max_bytes"`
	Inclusive bool   `json:"inclusive"`
}

type MCPTool struct {
	Name       string `json:"name"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	MaxRequest int64  `json:"max_request_bytes"`
}

type HTTPEndpoint struct {
	Method         string `json:"method"`
	Path           string `json:"path"`
	Authentication string `json:"authentication"`
	Payload        string `json:"payload"`
}

type SourceFamily struct {
	Name         string `json:"name"`
	RootEnv      string `json:"root_env"`
	DefaultRoot  string `json:"default_root"`
	StateEnv     string `json:"state_env"`
	DefaultState string `json:"default_state"`
}

type Environment struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Purpose  string `json:"purpose"`
}

type Entrypoint struct {
	Name        string   `json:"name"`
	Platform    string   `json:"platform"`
	Current     []string `json:"current"`
	Replacement []string `json:"replacement"`
}

func Parse(data []byte) (Contract, error) {
	var contract Contract
	if err := json.Unmarshal(data, &contract); err != nil {
		return Contract{}, fmt.Errorf("decode native compatibility contract: %w", err)
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func (c Contract) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("native compatibility contract version must equal %q", CurrentVersion)
	}
	if err := uniqueNonempty("payload limit", c.PayloadLimits, func(value PayloadLimit) string { return value.Name }); err != nil {
		return err
	}
	for _, value := range c.PayloadLimits {
		if value.MaxBytes <= 0 || !value.Inclusive {
			return fmt.Errorf("payload limit %q must be positive and inclusive", value.Name)
		}
	}
	if err := uniqueNonempty("MCP tool", c.MCPTools, func(value MCPTool) string { return value.Name }); err != nil {
		return err
	}
	for _, tool := range c.MCPTools {
		if tool.Method != "GET" && tool.Method != "POST" || !strings.HasPrefix(tool.Path, "/") || tool.MaxRequest <= 0 {
			return fmt.Errorf("MCP tool %q has an invalid transport binding", tool.Name)
		}
	}
	if err := uniqueNonempty("HTTP endpoint", c.HTTPEndpoints, func(value HTTPEndpoint) string { return value.Method + " " + value.Path }); err != nil {
		return err
	}
	for _, endpoint := range c.HTTPEndpoints {
		if (endpoint.Method != "GET" && endpoint.Method != "POST") || !strings.HasPrefix(endpoint.Path, "/") || endpoint.Authentication == "" || endpoint.Payload == "" {
			return fmt.Errorf("HTTP endpoint %q has an invalid contract", endpoint.Method+" "+endpoint.Path)
		}
	}
	if err := uniqueNonempty("source family", c.Sources, func(value SourceFamily) string { return value.Name }); err != nil {
		return err
	}
	for _, source := range c.Sources {
		if source.RootEnv == "" || source.DefaultRoot == "" || source.StateEnv == "" || source.DefaultState == "" {
			return fmt.Errorf("source family %q must declare root and state coordinates", source.Name)
		}
	}
	if err := uniqueNonempty("environment variable", c.Environment, func(value Environment) string { return value.Name }); err != nil {
		return err
	}
	for _, value := range c.Environment {
		if !strings.HasPrefix(value.Name, "CLAUDE_HISTORY_RAG_") || value.Purpose == "" {
			return fmt.Errorf("environment variable %q has an invalid contract", value.Name)
		}
	}
	if err := uniqueNonempty("entrypoint", c.Entrypoints, func(value Entrypoint) string { return value.Name }); err != nil {
		return err
	}
	for _, entrypoint := range c.Entrypoints {
		if entrypoint.Platform == "" || len(entrypoint.Current) == 0 || len(entrypoint.Replacement) == 0 {
			return fmt.Errorf("entrypoint %q must declare current and replacement commands", entrypoint.Name)
		}
	}
	return nil
}

func uniqueNonempty[T any](kind string, values []T, key func(T) string) error {
	if len(values) == 0 {
		return fmt.Errorf("native compatibility contract requires %s entries", kind)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		name := key(value)
		if name == "" {
			return fmt.Errorf("native compatibility contract has an unnamed %s", kind)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("native compatibility contract duplicates %s %q", kind, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}
