package drive

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

func TestBuiltinParamSpecsMatchSchema(t *testing.T) {
	data, err := os.ReadFile("../../schema/qrypt.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions map[string]struct {
			Driver   map[string]any `json:"x-driver"`
			Required []string       `json:"required"`
			AnyOf    []struct {
				Required []string `json:"required"`
			} `json:"anyOf"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}

	for defName, definition := range schema.Definitions {
		driver, _ := definition.Driver["type"].(string)
		if driver == "" {
			continue
		}

		spec, ok := builtinParamSpecs[driver]
		if !ok {
			t.Fatalf("%s: generated ParamSpec missing for driver %q", defName, driver)
		}

		var wantParams []string
		for name := range definition.Properties {
			wantParams = append(wantParams, name)
		}
		sort.Strings(wantParams)
		var gotParams []string
		for _, param := range spec.Params {
			gotParams = append(gotParams, param.Name)
		}
		sort.Strings(gotParams)
		if !reflect.DeepEqual(gotParams, wantParams) {
			t.Fatalf("%s: params = %v, want %v", defName, gotParams, wantParams)
		}

		if !reflect.DeepEqual(spec.Required, definition.Required) {
			t.Fatalf("%s: required = %v, want %v", defName, spec.Required, definition.Required)
		}

		var wantAnyOf [][]string
		for _, option := range definition.AnyOf {
			var group []string
			for _, name := range option.Required {
				if !contains(definition.Required, name) {
					group = append(group, name)
				}
			}
			if len(group) > 0 {
				wantAnyOf = append(wantAnyOf, group)
			}
		}
		if !reflect.DeepEqual(spec.AnyOf, wantAnyOf) {
			t.Fatalf("%s: anyOf = %v, want %v", defName, spec.AnyOf, wantAnyOf)
		}
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
