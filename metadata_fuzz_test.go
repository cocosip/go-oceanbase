package oceanbase

import "testing"

func FuzzMetadataFilters(f *testing.F) {
	f.Add(uint8(0), false, "APP", "x' OR 1=1 --")
	f.Add(uint8(1), true, "中文", "name/_%")
	f.Fuzz(func(t *testing.T, kind uint8, oracle bool, schema, name string) {
		if len(schema) > 4096 || len(name) > 4096 {
			t.Skip()
		}
		mode := ModeMySQL
		if oracle {
			mode = ModeOracle
		}
		kinds := []MetadataKind{MetadataColumns, MetadataForeignKeys, MetadataRoutineParameters, MetadataTypeAttributes}
		filter := MetadataFilter{Kind: kinds[int(kind)%len(kinds)], SchemaPattern: "s" + schema, NamePattern: "n" + name}
		plan, err := metadataPlan(mode, "4.3.5.0", filter)
		if err != nil {
			return
		}
		filter.SchemaPattern = "fixed-schema"
		filter.NamePattern = "fixed-name"
		fixed, err := metadataPlan(mode, "4.3.5.0", filter)
		if err != nil {
			t.Fatal(err)
		}
		if plan.query != fixed.query || len(plan.args) != 2 || plan.args[0].Value != "s"+schema || plan.args[1].Value != "n"+name {
			t.Fatal("filter changed SQL structure or lost its bound value")
		}
	})
}
