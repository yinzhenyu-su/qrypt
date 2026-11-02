package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEverySchemaDriverIsListed(t *testing.T) {
	path := filepath.Join("..", "..", "..", "schema", "qrypt.schema.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema struct {
		Definitions map[string]struct {
			XDriver struct {
				Type string `json:"type"`
			} `json:"x-driver"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	listed := make(map[string]bool, len(drivers))
	for _, spec := range drivers {
		if listed[spec.Name] {
			t.Fatalf("driver %q listed twice", spec.Name)
		}
		listed[spec.Name] = true
	}
	for name, definition := range schema.Definitions {
		driverType := definition.XDriver.Type
		if driverType == "" {
			continue
		}
		if !listed[driverType] {
			t.Errorf("schema definition %q declares driver %q, but gen-driver-capabilities does not list it", name, driverType)
		}
	}
}
