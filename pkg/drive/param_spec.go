package drive

// ParamSpec describes the parameters a driver accepts, including unconditional
// and alternative requirements.
type ParamSpec struct {
	Params   []ParamDef `json:"parameters"`
	Required []string   `json:"required,omitempty"`
	AnyOf    [][]string `json:"any_of,omitempty"`
}

// DescriptionFor returns the localized parameter description, falling back to
// English when the requested translation is missing.
func (p ParamDef) DescriptionFor(lang string) string {
	if description := p.DescriptionI18n[lang]; description != "" {
		return description
	}
	return p.Description
}

// ParamSpecFor returns the schema-derived parameter specification for name.
// Dynamically registered drivers fall back to the definitions passed to
// Register.
func ParamSpecFor(name string) ParamSpec {
	if spec, ok := builtinParamSpecs[name]; ok {
		return spec
	}

	registryMu.RLock()
	params := paramSchemas[name]
	registryMu.RUnlock()

	spec := ParamSpec{Params: params}
	for _, param := range params {
		if param.Required {
			spec.Required = append(spec.Required, param.Name)
		}
	}
	return spec
}
