// Package yamlpatch writes Stripe IDs back into gatr.yaml while
// preserving comments, ordering, and the operator's formatting
// choices (quotes, anchors, block vs flow style).
//
// It's NOT a general-purpose YAML mutator — the one job is `gatr push
// --auto-patch`: after the apply, set three kinds of fields:
//
//   - plans[i].billing.monthly.stripe_price_id
//   - plans[i].billing.annual.stripe_price_id
//   - metered_prices[i].stripe_meter_id
//
// Using yaml.v3's node tree (not Marshal/Unmarshal through structs)
// keeps comments intact — a round-trip through the Go schema.Config
// struct would drop every comment and reorder the keys.
package yamlpatch

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Kind enumerates the patch targets yamlpatch understands. Keeps the
// API type-safe without exposing raw yaml paths.
type Kind string

const (
	KindPlanMonthly  Kind = "plan_monthly"
	KindPlanAnnual   Kind = "plan_annual"
	KindMeteredPrice Kind = "metered_price"
)

// Patch is one field to set. YamlID is the primary key on the left-
// hand side (plan.id or metered_price.id). StripeID is the value to
// write into the target field; empty string is rejected (use yaml
// `null` manually if that's really what you mean).
type Patch struct {
	Kind     Kind
	YamlID   string
	StripeID string
}

// Apply reads src (the gatr.yaml bytes), applies each patch in order,
// and returns the rewritten bytes. The output is byte-for-byte
// identical to src except for:
//
//   - The targeted scalar values (set to the Stripe ID, double-quoted)
//   - A trailing newline if yaml.v3's serializer normalises one in
//
// Patches that don't find their target yaml_id are returned in the
// unresolved slice — the caller typically surfaces this as "no-op:
// plan `foo` not in yaml (skipped)" so typos don't silently succeed.
func Apply(src []byte, patches []Patch) (out []byte, unresolved []Patch, err error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, nil, fmt.Errorf("parse yaml: %w", err)
	}
	// Root is a DocumentNode → its only Content[0] is the top-level
	// MappingNode the gatr schema expects.
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, nil, fmt.Errorf("unexpected yaml root: kind=%d", root.Kind)
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("unexpected yaml top node: kind=%d", top.Kind)
	}

	for _, p := range patches {
		if p.StripeID == "" {
			return nil, nil, fmt.Errorf("patch %s:%s has empty StripeID", p.Kind, p.YamlID)
		}
		if !applyOne(top, p) {
			unresolved = append(unresolved, p)
		}
	}

	buf, err := marshal(&root)
	if err != nil {
		return nil, nil, err
	}
	return buf, unresolved, nil
}

// applyOne navigates to the right node and mutates it. Returns false
// if the yaml_id wasn't found (patch is unresolved; caller decides).
func applyOne(top *yaml.Node, p Patch) bool {
	switch p.Kind {
	case KindPlanMonthly, KindPlanAnnual:
		plans := findMappingValue(top, "plans")
		plan := findByID(plans, p.YamlID)
		if plan == nil {
			return false
		}
		billing := findMappingValue(plan, "billing")
		if billing == nil {
			return false
		}
		intervalKey := "monthly"
		if p.Kind == KindPlanAnnual {
			intervalKey = "annual"
		}
		interval := findMappingValue(billing, intervalKey)
		if interval == nil {
			return false
		}
		return setScalar(interval, "stripe_price_id", p.StripeID)

	case KindMeteredPrice:
		meters := findMappingValue(top, "metered_prices")
		mp := findByID(meters, p.YamlID)
		if mp == nil {
			return false
		}
		return setScalar(mp, "stripe_meter_id", p.StripeID)
	}
	return false
}

// findMappingValue returns the node at n[key] if n is a mapping with
// that key; nil otherwise.
func findMappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// findByID walks a sequence of mapping nodes and returns the first
// whose `id` matches. Works for both `plans` and `metered_prices` —
// both use `id` as the primary key.
func findByID(seq *yaml.Node, id string) *yaml.Node {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	for _, item := range seq.Content {
		if idNode := findMappingValue(item, "id"); idNode != nil && idNode.Value == id {
			return item
		}
	}
	return nil
}

// setScalar mutates (or inserts) a scalar field in a mapping. If the
// field exists, its value is rewritten in-place (preserves line
// position + surrounding comments). If missing, it's appended — which
// is unusual but valid (e.g. an older yaml without stripe_price_id:
// null).
func setScalar(mapping *yaml.Node, key, value string) bool {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			valNode := mapping.Content[i+1]
			valNode.Kind = yaml.ScalarNode
			valNode.Tag = "!!str"
			valNode.Style = yaml.DoubleQuotedStyle
			valNode.Value = value
			valNode.Alias = nil
			valNode.Content = nil
			return true
		}
	}
	// Missing field: append. Preserve the mapping's default style.
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: value},
	)
	return true
}

// marshal re-serialises the node tree with yaml.v3's default 2-space
// indent. Wrapped in a helper so future tuning (indent width, line
// width) is one place.
func marshal(n *yaml.Node) ([]byte, error) {
	var buf yamlBuf
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, fmt.Errorf("encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close yaml encoder: %w", err)
	}
	return buf.data, nil
}

// yamlBuf is a tiny bytes.Buffer replacement so we don't need to
// import bytes. Keeps the package dep graph tight.
type yamlBuf struct{ data []byte }

func (b *yamlBuf) Write(p []byte) (int, error) { b.data = append(b.data, p...); return len(p), nil }
