// Package pricing holds *reference* list prices of commercial LLM APIs.
//
// These numbers are only used to answer "what would this traffic have cost on
// a paid API?". They are estimates: real bills depend on prompt caching,
// batch discounts, tiers, and price changes after the file's as_of date.
package pricing

import (
	"encoding/json"
	"fmt"
	"os"
)

// Reference is one commercial model's list price, in USD per million tokens.
// The `json:"..."` tags tell encoding/json which JSON key maps to which field.
type Reference struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	Model         string  `json:"model"`
	InputPerMTok  float64 `json:"input_per_mtok_usd"`
	OutputPerMTok float64 `json:"output_per_mtok_usd"`
	SourceURL     string  `json:"source_url"`
}

// Cost returns the estimated USD cost of the given token counts.
func (r Reference) Cost(promptTokens, completionTokens int64) float64 {
	return float64(promptTokens)/1e6*r.InputPerMTok +
		float64(completionTokens)/1e6*r.OutputPerMTok
}

// Table is the whole pricing file.
type Table struct {
	AsOf             string      `json:"as_of"`
	Currency         string      `json:"currency"`
	Disclaimer       string      `json:"disclaimer"`
	DefaultReference string      `json:"default_reference"`
	References       []Reference `json:"references"`
}

// Load reads and validates a pricing JSON file.
func Load(path string) (*Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pricing file: %w", err)
	}
	var t Table
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse pricing file %s: %w", path, err)
	}
	if err := t.validate(); err != nil {
		return nil, fmt.Errorf("pricing file %s: %w", path, err)
	}
	return &t, nil
}

func (t *Table) validate() error {
	if len(t.References) == 0 {
		return fmt.Errorf("no references defined")
	}
	seen := map[string]bool{}
	for _, r := range t.References {
		if r.ID == "" {
			return fmt.Errorf("reference with empty id")
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate reference id %q", r.ID)
		}
		seen[r.ID] = true
		if r.InputPerMTok < 0 || r.OutputPerMTok < 0 {
			return fmt.Errorf("reference %q has a negative price", r.ID)
		}
	}
	if t.DefaultReference == "" {
		t.DefaultReference = t.References[0].ID
	} else if !seen[t.DefaultReference] {
		return fmt.Errorf("default_reference %q not found in references", t.DefaultReference)
	}
	return nil
}
