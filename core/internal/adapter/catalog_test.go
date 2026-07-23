package adapter

import "testing"

func TestEmbeddedCatalog(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	manifest, ok := catalog.Find("codex")
	if !ok || manifest.SupportLevel != "pilot" {
		t.Fatalf("codex manifest missing: %#v", manifest)
	}
}

func TestCatalogRejectsInvalidCombinations(t *testing.T) {
	_, err := ParseCatalog([]byte(`{
		"version":1,
		"adapters":[
			{"id":"x","displayName":"X","phase1":true,"supportLevel":"unsupported",
			 "surfaces":["cli"],"platforms":["linux"],"dialect":null,"events":[]}
		]
	}`))
	if err == nil {
		t.Fatal("expected invalid unsupported adapter")
	}
}

func TestCatalogValidationErrors(t *testing.T) {
	values := []string{
		`{"version":2,"adapters":[]}`,
		`{"version":1,"adapters":[{"id":"","displayName":"X","supportLevel":"verified"}]}`,
		`{"version":1,"adapters":[
			{"id":"x","displayName":"X","supportLevel":"verified","surfaces":["cli"],"platforms":["linux"],"dialect":"codex-json-hooks","events":[]},
			{"id":"x","displayName":"X","supportLevel":"verified","surfaces":["cli"],"platforms":["linux"],"dialect":"codex-json-hooks","events":[]}
		]}`,
		`{"version":1,"adapters":[{"id":"x","displayName":"X","supportLevel":"mystery","surfaces":["cli"],"platforms":["linux"],"dialect":"codex-json-hooks","events":[]}]}`,
		`{"version":1,"adapters":[{"id":"x","displayName":"X","supportLevel":"verified","surfaces":[],"platforms":["linux"],"dialect":"codex-json-hooks","events":[]}]}`,
		`{"version":1,"adapters":[{"id":"x","displayName":"X","supportLevel":"verified","surfaces":["cli"],"platforms":["linux"],"dialect":"mystery","events":[]}]}`,
	}
	for index, value := range values {
		if _, err := ParseCatalog([]byte(value)); err == nil {
			t.Fatalf("invalid catalog %d was accepted", index)
		}
	}
	if _, err := ParseCatalog([]byte("{")); err == nil {
		t.Fatal("expected JSON parse error")
	}
}
