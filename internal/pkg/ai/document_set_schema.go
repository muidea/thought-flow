package ai

import (
	"reflect"
	"strings"

	ds "thoughtflow/internal/pkg/documentset"
)

// DocumentSetPlanningInput is shared with the durable call budget so schema
// guidance consumes the same input allowance as the rest of the actual payload.
func DocumentSetPlanningInput(c ds.CaptureSnapshot, profile ds.Profile, previous *ds.SharedDesign) map[string]any {
	indices := []int{}
	for i, message := range c.Messages {
		if message.Role == "user" {
			indices = append(indices, i)
		}
	}
	schema := sharedDesignResponseSchema()
	if ds.Engineering(profile) {
		defs := schema["$defs"].(map[string]any)
		relation := defs["Relation"].(map[string]any)
		properties := relation["properties"].(map[string]any)
		kinds := []string{}
		for _, rule := range ds.EngineeringRelationRules() {
			kinds = append(kinds, rule.Kind)
		}
		properties["kind"].(map[string]any)["enum"] = kinds
		properties["from"].(map[string]any)["description"] = "Existing fact ID on the from_kinds side of relation_contract; direction is meaningful."
		properties["to"].(map[string]any)["description"] = "Existing fact ID on the to_kinds side; supersedes instead requires a fixed historical reference."
	}
	input := map[string]any{"capture": c, "profile": profile, "previous": previous, "contract_schema": ds.EngineeringSchema(profile), "response_schema": schema,
		"source_contract": map[string]any{"message_index_base": 0, "message_index_space": "capture.messages including ALL roles; never a user-turn ordinal", "user_message_indices": indices, "quote_policy": "copy an exact contiguous substring of capture.messages[source.message].text; do not paraphrase, summarize, escape twice, or quote assistant messages"}}
	if ds.Engineering(profile) {
		input["relation_contract"] = ds.EngineeringRelationRules()
	}
	return input
}

// Generate the planner's wire schema from the same DTOs StrictJSON decodes.
// Definitions avoid repeating large typed contracts; this is prompt context,
// not a provider capability request or a replacement for business validation.
func sharedDesignResponseSchema() map[string]any {
	return responseSchema(reflect.TypeOf(ds.SharedDesign{}))
}

func captureResponseSchema() map[string]any {
	return responseSchema(reflect.TypeOf(captureContextJSON{}))
}

func responseSchema(t reflect.Type) map[string]any {
	defs := map[string]any{}
	root := responseTypeSchema(t, defs)
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["$defs"] = defs
	return root
}

// This helper supports the wire types used by SharedDesign and Capture.
// New unsupported types must be handled explicitly, never described as any JSON.
func responseTypeSchema(t reflect.Type, defs map[string]any) map[string]any {
	switch t.Kind() {
	case reflect.Pointer:
		return map[string]any{"anyOf": []any{responseTypeSchema(t.Elem(), defs), map[string]any{"type": "null"}}}
	case reflect.Struct:
		name := t.Name()
		if _, exists := defs[name]; !exists {
			properties := map[string]any{}
			required := []string{}
			definition := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
			defs[name] = definition
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				if !field.IsExported() {
					continue
				}
				tag := strings.Split(field.Tag.Get("json"), ",")
				if tag[0] == "-" {
					continue
				}
				key := tag[0]
				if key == "" {
					key = field.Name
				}
				properties[key] = responseTypeSchema(field.Type, defs)
				if !strings.Contains(","+field.Tag.Get("json")+",", ",omitempty,") {
					required = append(required, key)
				}
			}
			definition["required"] = required
			if t == reflect.TypeOf(ds.ArtifactContract{}) {
				// ArtifactContract's decoder permits only this information-free
				// planner placeholder; it cannot waive a substantive field.
				properties["not_applicable"] = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false, "maxProperties": 0}
			}
		}
		return map[string]any{"$ref": "#/$defs/" + name}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": responseTypeSchema(t.Elem(), defs)}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic("response schema requires string map keys")
		}
		return map[string]any{"type": "object", "additionalProperties": responseTypeSchema(t.Elem(), defs)}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Uint64:
		return map[string]any{"type": "integer"}
	default:
		panic("unsupported response schema type: " + t.String())
	}
}
