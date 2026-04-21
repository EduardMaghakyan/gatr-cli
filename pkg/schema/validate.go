package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"
)

var (
	compiledSchema     *jsonschema.Schema
	compiledSchemaOnce sync.Once
)

func compiledSchemaFor() *jsonschema.Schema {
	compiledSchemaOnce.Do(func() {
		var raw any
		if err := json.Unmarshal(SchemaJSON, &raw); err != nil {
			panic(fmt.Sprintf("gatr: embedded schema JSON is invalid: %v", err))
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(SchemaURI, raw); err != nil {
			panic(fmt.Sprintf("gatr: cannot register embedded schema: %v", err))
		}
		s, err := c.Compile(SchemaURI)
		if err != nil {
			panic(fmt.Sprintf("gatr: cannot compile embedded schema: %v", err))
		}
		compiledSchema = s
	})
	return compiledSchema
}

// Validate runs the embedded JSON Schema against the raw YAML bytes.
// Returns a *schema.Error with a Gatr error code on failure.
func Validate(data []byte) error {
	s := compiledSchemaFor()
	jsonBytes, err := yaml.YAMLToJSON(data)
	if err != nil {
		return &Error{Code: "E001", Message: err.Error()}
	}
	var instance any
	if err := json.Unmarshal(jsonBytes, &instance); err != nil {
		return &Error{Code: "E001", Message: err.Error()}
	}
	if err := s.Validate(instance); err != nil {
		return &Error{Code: classifyValidationError(err), Message: err.Error()}
	}
	return nil
}

func classifyValidationError(err error) string {
	var ve *jsonschema.ValidationError
	// jsonschema always returns ValidationError on failure; the !errors.As
	// branch is unreachable in practice and kept for safety only.
	_ = errors.As(err, &ve)
	if ve == nil {
		return "E010"
	}
	for _, cause := range allCauses(ve) {
		path := "/" + strings.Join(cause.InstanceLocation, "/")
		msg := cause.Error()
		switch {
		case path == "/version":
			return "E003"
		case strings.Contains(msg, "missing property 'version'"):
			return "E003"
		case strings.Contains(path, "/limits/") && !strings.HasSuffix(path, "/limits"):
			return "E013"
		case strings.HasSuffix(path, "per_seat_pricing"):
			return "E014"
		}
	}
	return "E010"
}

func allCauses(ve *jsonschema.ValidationError) []*jsonschema.ValidationError {
	out := []*jsonschema.ValidationError{ve}
	for _, c := range ve.Causes {
		out = append(out, allCauses(c)...)
	}
	return out
}
