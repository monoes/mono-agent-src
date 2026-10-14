package workflow

import (
	"fmt"
	"strings"
)

// controlValue is the effective value of a controlling key: its config
// value, else the default of a field with that key that applies (one with
// a condition before an unconditional one: a resource-scoped operation
// select over the native one), else "". known is false when the value is
// an expression.
func controlValue(n WorkflowNode, s *NodeSchema, key string, depth int) (val string, known bool) {
	if v, ok := n.Config[key]; ok && !isEmptyValue(v) {
		if isExpression(v) {
			return "", false
		}
		return fmt.Sprint(v), true
	}
	if depth > 3 {
		return "", true
	}
	fallback := ""
	for i := range s.Fields {
		f := &s.Fields[i]
		if f.Key != key || f.Default == nil {
			continue
		}
		if f.DependsOn == nil && len(f.VisibleFor) == 0 {
			if fallback == "" {
				fallback = fmt.Sprint(f.Default)
			}
			continue
		}
		if holds, ok := conditionsHold(n, s, f, depth+1); holds && ok {
			return fmt.Sprint(f.Default), true
		}
	}
	return fallback, true
}

// dependencyHolds reports whether a field's depends_on condition is met;
// known is false when the controlling value is an expression.
func dependencyHolds(n WorkflowNode, s *NodeSchema, dep *FieldDependency, depth int) (holds, known bool) {
	if dep == nil || dep.Key == "" {
		return true, true
	}
	sv, ok := controlValue(n, s, dep.Key, depth)
	if !ok {
		return false, false
	}
	for _, want := range dep.Values {
		if strings.EqualFold(sv, want) {
			return true, true
		}
	}
	return false, true
}

// visibleForHolds reports whether a field's visible_for matches: empty, or
// one entry whose every key has exactly that effective value.
func visibleForHolds(n WorkflowNode, s *NodeSchema, vf []map[string]string, depth int) (holds, known bool) {
	if len(vf) == 0 {
		return true, true
	}
	known = true
	for _, entry := range vf {
		match := true
		for k, want := range entry {
			sv, ok := controlValue(n, s, k, depth)
			if !ok {
				known, match = false, false
				break
			}
			if sv != want {
				match = false
				break
			}
		}
		if match {
			return true, true
		}
	}
	return false, known
}

func conditionsHold(n WorkflowNode, s *NodeSchema, f *NodeSchemaField, depth int) (holds, known bool) {
	if holds, known := dependencyHolds(n, s, f.DependsOn, depth); !holds || !known {
		return holds, known
	}
	return visibleForHolds(n, s, f.VisibleFor, depth)
}

// fieldApplies reports whether f is shown for n's config: its depends_on
// and visible_for both hold. known is false when a controlling value is
// an expression; callers then raise no issue for f.
func fieldApplies(n WorkflowNode, s *NodeSchema, f *NodeSchemaField) (applies, known bool) {
	return conditionsHold(n, s, f, 0)
}
