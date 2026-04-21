package schema

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

// Parse reads YAML bytes and unmarshals into a typed Config.
// It does not run JSON Schema validation — call Validate or ParseAndValidate.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, &Error{Code: "E001", Message: err.Error()}
	}
	return &cfg, nil
}

// ParseFile reads a file from disk and parses it.
func ParseFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{Code: "E002", Message: err.Error(), Path: path}
	}
	return Parse(data)
}

// ParseAndValidate parses YAML bytes, validates against the embedded JSON
// Schema, runs the semantic refinements (which JSON Schema cannot express),
// and returns a typed Config on success.
func ParseAndValidate(data []byte) (*Config, error) {
	if err := Validate(data); err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := validateRefinements(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ParseFileAndValidate is the file-path counterpart of ParseAndValidate.
func ParseFileAndValidate(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{Code: "E002", Message: err.Error(), Path: path}
	}
	return ParseAndValidate(data)
}

// IDExists is a helper for round-trip tests.
func (c *Config) IDExists(scope, id string) bool {
	switch scope {
	case "features":
		for _, f := range c.Features {
			if f.ID == id {
				return true
			}
		}
	case "limits":
		for _, l := range c.Limits {
			if l.ID == id {
				return true
			}
		}
	case "plans":
		for _, p := range c.Plans {
			if p.ID == id {
				return true
			}
		}
	default:
		panic(fmt.Sprintf("unknown scope: %q", scope))
	}
	return false
}
