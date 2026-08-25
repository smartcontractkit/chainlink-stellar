package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type specEntry struct {
	FunctionV0     *specFunction `json:"function_v0"`
	UdtStructV0    *specStruct   `json:"udt_struct_v0"`
	UdtUnionV0     *specUnion    `json:"udt_union_v0"`
	UdtEnumV0      *specEnum     `json:"udt_enum_v0"`
	UdtErrorEnumV0 *specEnum     `json:"udt_error_enum_v0"`
	EventV0        *specEvent    `json:"event_v0"`
}

type specFunction struct {
	Name    string            `json:"name"`
	Inputs  []specParam       `json:"inputs"`
	Outputs []json.RawMessage `json:"outputs"`
}

type specParam struct {
	Name string          `json:"name"`
	Type json.RawMessage `json:"type_"`
}

type specStruct struct {
	Name   string      `json:"name"`
	Fields []specParam `json:"fields"`
}

type specUnion struct {
	Name  string          `json:"name"`
	Cases []specUnionCase `json:"cases"`
}

type specUnionCase struct {
	VoidV0 *struct {
		Name string `json:"name"`
	} `json:"void_v0"`
	TupleV0 *struct {
		Name string            `json:"name"`
		Type []json.RawMessage `json:"type_"`
	} `json:"tuple_v0"`
}

type specEnum struct {
	Name  string `json:"name"`
	Cases []struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	} `json:"cases"`
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

func ParseSpecJSON(input []byte) (*Contract, error) {
	var entries []specEntry
	if err := json.Unmarshal(input, &entries); err != nil {
		return nil, fmt.Errorf("decode spec JSON: %w", err)
	}
	contract := &Contract{}
	seenEvents := map[string]int{}
	for _, e := range entries {
		switch {
		case e.FunctionV0 != nil:
			fn := e.FunctionV0
			if strings.HasPrefix(fn.Name, "__") {
				continue
			}
			f := Function{Name: fn.Name}
			for _, in := range fn.Inputs {
				t, err := specTypeString(in.Type)
				if err != nil {
					return nil, fmt.Errorf("function %s input %s: %w", fn.Name, in.Name, err)
				}
				f.Inputs = append(f.Inputs, Field{Name: in.Name, Type: t})
			}
			ret, err := specOutputsString(fn.Outputs)
			if err != nil {
				return nil, fmt.Errorf("function %s outputs: %w", fn.Name, err)
			}
			f.Returns = ret
			contract.Functions = append(contract.Functions, f)
		case e.UdtStructV0 != nil:
			st := e.UdtStructV0
			s := Struct{Name: st.Name}
			for _, fld := range st.Fields {
				t, err := specTypeString(fld.Type)
				if err != nil {
					return nil, fmt.Errorf("struct %s field %s: %w", st.Name, fld.Name, err)
				}
				s.Fields = append(s.Fields, Field{Name: fld.Name, Type: t})
			}
			contract.Structs = append(contract.Structs, s)
		case e.UdtUnionV0 != nil:
			u := e.UdtUnionV0
			en := Enum{Name: u.Name, Union: true}
			for _, c := range u.Cases {
				switch {
				case c.TupleV0 != nil:
					v := EnumVariant{Name: c.TupleV0.Name, Kind: EnumVariantTuple}
					for _, rt := range c.TupleV0.Type {
						t, err := specTypeString(rt)
						if err != nil {
							return nil, fmt.Errorf("union %s case %s: %w", u.Name, c.TupleV0.Name, err)
						}
						v.Payload = append(v.Payload, Field{Type: t})
					}
					en.Variants = append(en.Variants, v)
				case c.VoidV0 != nil:
					en.Variants = append(en.Variants, EnumVariant{Name: c.VoidV0.Name, Kind: EnumVariantUnit})
				default:
					return nil, fmt.Errorf("union %s: unsupported case shape", u.Name)
				}
			}
			contract.Enums = append(contract.Enums, en)
		case e.UdtEnumV0 != nil:
			en := Enum{Name: e.UdtEnumV0.Name}
			for _, c := range e.UdtEnumV0.Cases {
				en.Variants = append(en.Variants, EnumVariant{Name: c.Name, Kind: EnumVariantUnit, Value: c.Value})
			}
			contract.Enums = append(contract.Enums, en)
		case e.UdtErrorEnumV0 != nil:
			ee := ErrorEnum{Name: e.UdtErrorEnumV0.Name}
			for _, c := range e.UdtErrorEnumV0.Cases {
				ee.Variants = append(ee.Variants, ErrorVariant{Name: c.Name, Value: c.Value})
			}
			contract.Errors = append(contract.Errors, ee)
		case e.EventV0 != nil:
			ev := e.EventV0
			if idx, ok := seenEvents[ev.Name]; ok {
				existing := contract.Events[idx]
				if isAuthTopics(existing.Topics) && !isAuthTopics(ev.PrefixTopics) {
					fmt.Fprintf(os.Stderr, "WARN: event name %s: replacing auth-library event (topics %v) with the contract's own (topics %v)\n", ev.Name, existing.Topics, ev.PrefixTopics)
				} else {
					fmt.Fprintf(os.Stderr, "WARN: skipping event %s (topics %v): name collides with earlier event (topics %v); Go cannot hold two types with one name\n", ev.Name, ev.PrefixTopics, existing.Topics)
					continue
				}
				replacement, err := specEventToEvent(ev)
				if err != nil {
					return nil, err
				}
				contract.Events[idx] = replacement
				continue
			}
			seenEvents[ev.Name] = len(contract.Events)
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
			contract.Events = append(contract.Events, event)
		}
	}
	return contract, nil
}

func isAuthTopics(topics []string) bool {
	for _, t := range topics {
		if strings.HasPrefix(t, "auth_") {
			return true
		}
	}
	return false
}

func specEventToEvent(ev *specEvent) (Event, error) {
	event := Event{Name: ev.Name, Topics: ev.PrefixTopics, DataFormat: specDataFormat(ev.DataFormat)}
	for _, p := range ev.Params {
		t, err := specTypeString(p.Type)
		if err != nil {
			return Event{}, fmt.Errorf("event %s param %s: %w", ev.Name, p.Name, err)
		}
		event.Fields = append(event.Fields, Field{Name: p.Name, Type: t, Topic: p.Location == "topic_list"})
	}
	return event, nil
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

func specOutputsString(outputs []json.RawMessage) (string, error) {
	switch len(outputs) {
	case 0:
		return "", nil
	case 1:
		return specTypeString(outputs[0])
	default:
		parts := make([]string, len(outputs))
		for i, o := range outputs {
			t, err := specTypeString(o)
			if err != nil {
				return "", err
			}
			parts[i] = t
		}
		return "(" + strings.Join(parts, ", ") + ")", nil
	}
}

func specTypeString(raw json.RawMessage) (string, error) {
	var scalar string
	if err := json.Unmarshal(raw, &scalar); err == nil {
		switch scalar {
		case "bool", "u32", "i32", "u64", "i64", "u128", "i128":
			return scalar, nil
		case "void":
			return "()", nil
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
		case "muxed_address":
			return "soroban_sdk::MuxedAddress", nil
		case "timepoint", "duration":
			return "", fmt.Errorf("spec type %q has no Go mapping yet", scalar)
		default:
			return "", fmt.Errorf("unknown scalar spec type %q", scalar)
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
		case "map":
			var v struct {
				KeyType   json.RawMessage `json:"key_type"`
				ValueType json.RawMessage `json:"value_type"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return "", err
			}
			k, err := specTypeString(v.KeyType)
			if err != nil {
				return "", err
			}
			val, err := specTypeString(v.ValueType)
			if err != nil {
				return "", err
			}
			return "soroban_sdk::Map<" + k + ", " + val + ">", nil
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
		case "result":
			var v struct {
				OkType    json.RawMessage `json:"ok_type"`
				ErrorType json.RawMessage `json:"error_type"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return "", err
			}
			ok, err := specTypeString(v.OkType)
			if err != nil {
				return "", err
			}
			e, err := specTypeString(v.ErrorType)
			if err != nil {
				return "", err
			}
			return "Result<" + ok + ", " + e + ">", nil
		default:
			return "", fmt.Errorf("unsupported spec type kind %q", kind)
		}
	}
	return "", fmt.Errorf("empty spec type object")
}
