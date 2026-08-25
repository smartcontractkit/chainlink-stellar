package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type specEventEntry struct {
	EventV0 *specEvent `json:"event_v0"`
}

type specEvent struct {
	Name         string   `json:"name"`
	PrefixTopics []string `json:"prefix_topics"`
	Params       []struct {
		Name     string          `json:"name"`
		Type     json.RawMessage `json:"type_"`
		Location string          `json:"location"`
	} `json:"params"`
	DataFormat string `json:"data_format"`
}

func ParseSpecEvents(input []byte) ([]Event, error) {
	var entries []specEventEntry
	if err := json.Unmarshal(input, &entries); err != nil {
		return nil, fmt.Errorf("decode contract spec JSON: %w", err)
	}
	var events []Event
	for _, entry := range entries {
		ev := entry.EventV0
		if ev == nil {
			continue
		}
		event := Event{Name: ev.Name, Topics: ev.PrefixTopics, DataFormat: specDataFormat(ev.DataFormat)}
		for _, p := range ev.Params {
			t, err := specTypeString(p.Type)
			if err != nil {
				return nil, fmt.Errorf("event %s param %s: %w", ev.Name, p.Name, err)
			}
			event.Fields = append(event.Fields, Field{
				Name:  p.Name,
				Type:  t,
				Topic: p.Location == "topic_list",
			})
		}
		events = append(events, event)
	}
	return events, nil
}

func specDataFormat(f string) string {
	switch f {
	case "single_value":
		return "single-value"
	case "map", "":
		return ""
	default:
		return f
	}
}

func specTypeString(raw json.RawMessage) (string, error) {
	var scalar string
	if err := json.Unmarshal(raw, &scalar); err == nil {
		switch scalar {
		case "bool", "u32", "i32", "u64", "i64", "u128", "i128":
			return scalar, nil
		case "u256":
			return "soroban_sdk::U256", nil
		case "i256":
			return "soroban_sdk::I256", nil
		case "bytes":
			return "soroban_sdk::Bytes", nil
		case "string":
			return "soroban_sdk::String", nil
		case "symbol":
			return "soroban_sdk::Symbol", nil
		case "address":
			return "soroban_sdk::Address", nil
		default:
			return "", fmt.Errorf("spec scalar type %q has no mapping", scalar)
		}
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("unrecognized spec type %s", string(raw))
	}
	for kind, body := range obj {
		switch kind {
		case "bytes_n":
			var b struct {
				N int `json:"n"`
			}
			if err := json.Unmarshal(body, &b); err != nil {
				return "", err
			}
			return fmt.Sprintf("soroban_sdk::BytesN<%d>", b.N), nil
		case "vec":
			var v struct {
				ElementType json.RawMessage `json:"element_type"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return "", err
			}
			inner, err := specTypeString(v.ElementType)
			if err != nil {
				return "", err
			}
			return "soroban_sdk::Vec<" + inner + ">", nil
		case "option":
			var v struct {
				ValueType json.RawMessage `json:"value_type"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return "", err
			}
			inner, err := specTypeString(v.ValueType)
			if err != nil {
				return "", err
			}
			return "Option<" + inner + ">", nil
		case "udt":
			var v struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return "", err
			}
			return v.Name, nil
		case "tuple":
			var v struct {
				ValueTypes []json.RawMessage `json:"value_types"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return "", err
			}
			parts := make([]string, len(v.ValueTypes))
			for i, vt := range v.ValueTypes {
				t, err := specTypeString(vt)
				if err != nil {
					return "", err
				}
				parts[i] = t
			}
			return "(" + strings.Join(parts, ", ") + ")", nil
		default:
			return "", fmt.Errorf("spec type kind %q has no mapping", kind)
		}
	}
	return "", fmt.Errorf("empty spec type object")
}
