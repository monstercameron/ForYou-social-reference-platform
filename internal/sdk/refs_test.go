package sdk

import (
	"encoding/json"
	"os"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func TestPlanReferences(t *testing.T) {
	t.Run("fixture activates declarations and reports missing ID", func(t *testing.T) {
		data, err := os.ReadFile("testdata/as/post-with-refs.json")
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Message wire.PostMessage `json:"message"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatal(err)
		}
		got, err := PlanReferences(f.Message.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Refs) != 2 || got.Refs[0].ID != "r1" || got.Refs[0].Target != "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG" || got.Refs[0].Kind != RefAuthor || !got.Refs[0].Used || got.Refs[1].ID != "r2" || got.Refs[1].Target != "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f" || got.Refs[1].Kind != RefMessage || !got.Refs[1].Used || len(got.Missing) != 1 || got.Missing[0] != "missing" {
			t.Fatalf("plan = %#v", got)
		}
		p := f.Message.Payload
		p.Text += "\n\n[again](fy-ref:missing)"
		deduplicated, err := PlanReferences(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(deduplicated.Missing) != 1 || deduplicated.Missing[0] != "missing" {
			t.Fatalf("deduplicated missing IDs = %v", deduplicated.Missing)
		}
	})
}
