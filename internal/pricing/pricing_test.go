package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestCost(t *testing.T) {
	ref := Reference{InputPerMTok: 2, OutputPerMTok: 10}
	// 1M prompt tokens * $2 + 500k completion tokens * $10/M = $2 + $5
	got := ref.Cost(1_000_000, 500_000)
	if math.Abs(got-7) > 1e-9 {
		t.Errorf("Cost = %f, want 7", got)
	}
}

func TestSeedFileLoads(t *testing.T) {
	tbl, err := Load("../../data/pricing.seed.json")
	if err != nil {
		t.Fatalf("seed pricing file should load: %v", err)
	}
	if len(tbl.References) < 2 || tbl.AsOf == "" || tbl.Disclaimer == "" {
		t.Errorf("seed file looks incomplete: %+v", tbl)
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	cases := map[string]string{
		"empty refs":      `{"references": []}`,
		"duplicate id":    `{"references":[{"id":"a"},{"id":"a"}]}`,
		"negative price":  `{"references":[{"id":"a","input_per_mtok_usd":-1}]}`,
		"bad default ref": `{"default_reference":"zzz","references":[{"id":"a"}]}`,
		"not json":        `nope`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "p.json")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Errorf("expected an error for %s", name)
			}
		})
	}
}
