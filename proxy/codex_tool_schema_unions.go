package proxy

import (
	"encoding/json"
	"math/big"
	"strconv"
)

// MCP 服务器常把枚举写成带说明的常量联合：
//
//	"action": {"type":"string","oneOf":[{"const":"a","description":"..."}, ...]}
//
// 分支一多（实测 13 支）Codex 上游会静默中止：HTTP 200 + response.incomplete、
// 0 输出 token、无任何内容，且任意去掉一支就恢复。这种联合与 enum 语义完全等价，
// 这里把它折成 enum 后再发上游。只有能证明等价的形态才动：每一支只含
// const（可带 description/title）、常量互不重复、oneOf/anyOf 不同时出现；已有
// enum 时必须与常量集合一致才删掉联合，否则原样保留。
const codexConstUnionFoldThreshold = 8

// simplifyConstUnionSchemas 递归折叠 schema 及其全部子 schema 里的纯常量联合。
func simplifyConstUnionSchemas(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	changed := foldPureConstUnion(schema)
	forEachSubSchema(schema, func(child map[string]any) {
		if simplifyConstUnionSchemas(child) {
			changed = true
		}
	})
	return changed
}

func foldPureConstUnion(schema map[string]any) bool {
	_, hasOneOf := schema["oneOf"]
	_, hasAnyOf := schema["anyOf"]
	if hasOneOf == hasAnyOf {
		// 两者都没有，或同时出现（复合约束，语义不再是单纯的枚举）。
		return false
	}
	unionKey := "oneOf"
	if hasAnyOf {
		unionKey = "anyOf"
	}
	branches, ok := schema[unionKey].([]any)
	if !ok || len(branches) < codexConstUnionFoldThreshold {
		return false
	}
	values := make([]any, 0, len(branches))
	seen := make(map[string]struct{}, len(branches))
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		constValue, ok := branch["const"]
		if !ok {
			return false
		}
		for key := range branch {
			if key != "const" && key != "description" && key != "title" {
				return false
			}
		}
		canonical, ok := canonicalSchemaConstKey(constValue)
		if !ok {
			return false
		}
		if _, dup := seen[canonical]; dup {
			return false
		}
		seen[canonical] = struct{}{}
		values = append(values, constValue)
	}
	if rawEnum, exists := schema["enum"]; exists {
		enum, ok := rawEnum.([]any)
		if !ok || len(enum) != len(values) {
			return false
		}
		enumSeen := make(map[string]struct{}, len(enum))
		for _, value := range enum {
			canonical, ok := canonicalSchemaConstKey(value)
			if !ok {
				return false
			}
			if _, dup := enumSeen[canonical]; dup {
				return false
			}
			if _, matched := seen[canonical]; !matched {
				return false
			}
			enumSeen[canonical] = struct{}{}
		}
		delete(schema, unionKey)
		return true
	}
	schema["enum"] = values
	delete(schema, unionKey)
	return true
}

// canonicalSchemaConstKey 给标量常量一个类型限定的比较键；对象/数组常量不折。
func canonicalSchemaConstKey(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return "s:" + typed, true
	case float64:
		return "n:" + strconv.FormatFloat(typed, 'g', -1, 64), true
	case json.Number:
		var ratio big.Rat
		if _, ok := ratio.SetString(typed.String()); ok {
			return "n:" + ratio.RatString(), true
		}
		return "n:" + typed.String(), true
	case bool:
		if typed {
			return "b:true", true
		}
		return "b:false", true
	case nil:
		return "null", true
	}
	return "", false
}
