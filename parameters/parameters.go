package parameters

import (
	"fmt"
	"strings"
)

const (
	InputModeNone    = "none"
	InputModeService = "service"
	InputModeAll     = "all"

	FieldNameDefault  = "lower_camel_case"
	FieldNamePreserve = "preserve"

	JS64BitTypeString = "string"
	JS64BitTypeNumber = "number"
)

type Parameters struct {
	WrappersAsNull        bool
	InputMode             string
	JS64BitType           string
	RootTypePrefix        *string
	FieldName             string
	TrimPrefixes          []string
	NullableListTypes     bool
	DisableAllPrefixes    bool
	MapWellKnownTypes     map[string]string
	DetectRequestMessages bool
}

func NewParameters(parameter string) (*Parameters, error) {
	params := &Parameters{MapWellKnownTypes: make(map[string]string)}

	parts := strings.Split(parameter, ",")
	for _, part := range parts {
		if part == "" {
			continue
		}

		keyValue := strings.SplitN(part, "=", 2)
		key := keyValue[0]
		var value string
		if len(keyValue) == 2 {
			value = keyValue[1]
		}

		switch key {
		case "null_wrappers":
			params.WrappersAsNull = true
		case "nullable_list_types":
			params.NullableListTypes = true
		case "disable_all_prefixes":
			params.DisableAllPrefixes = true
		case "detect_request_messages":
			params.DetectRequestMessages = true
		case "input_mode":
			params.InputMode = value
		case "js_64bit_type":
			params.JS64BitType = value
		case "root_type_prefix":
			params.RootTypePrefix = &value
		case "field_name":
			if value != FieldNamePreserve {
				value = FieldNameDefault
			}
			params.FieldName = value
		case "trim_prefix":
			params.TrimPrefixes = append(params.TrimPrefixes, value)
		case "map_wellknown_type":
			pair := strings.Split(value, "=")
			if len(pair) != 2 {
				return nil, fmt.Errorf("maps of well-known type must be in the format of 'key=value'")
			}

			name := pair[0]
			if !strings.HasPrefix(name, ".") {
				name = "." + name
			}

			params.MapWellKnownTypes[name] = pair[1]
		}
	}

	if params.InputMode == "" {
		params.InputMode = InputModeService
	}

	return params, nil
}
