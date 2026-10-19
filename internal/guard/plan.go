package guard

import (
	"encoding/json"
	"fmt"
	"slices"
)

// PlanRelations extracts every relation an EXPLAIN (VERBOSE, FORMAT JSON)
// plan reads. Views are expanded by the planner, so the result names the
// underlying tables. It fails closed: a scan node without a schema is an
// error, since the relation cannot be checked.
func PlanRelations(plan []byte) ([]Relation, error) {
	var top []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err := json.Unmarshal(plan, &top); err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}
	var rels []Relation
	var walk func(node map[string]any) error
	walk = func(node map[string]any) error {
		if name, ok := node["Relation Name"].(string); ok {
			schema, _ := node["Schema"].(string)
			if schema == "" {
				return fmt.Errorf("%w: plan node for %q has no schema", ErrDenied, name)
			}
			r := Relation{Schema: schema, Name: name}
			if !slices.Contains(rels, r) {
				rels = append(rels, r)
			}
		}
		children, _ := node["Plans"].([]any)
		for _, c := range children {
			child, ok := c.(map[string]any)
			if !ok {
				return fmt.Errorf("parse plan: unexpected child node %T", c)
			}
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, t := range top {
		if t.Plan == nil {
			return nil, fmt.Errorf("parse plan: missing Plan")
		}
		if err := walk(t.Plan); err != nil {
			return nil, err
		}
	}
	return rels, nil
}
