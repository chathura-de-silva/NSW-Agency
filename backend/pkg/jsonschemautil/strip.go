package jsonschemautil

// Please update docs/jsonschemautil.md if you change the supported subset of JSON Schema.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// ErrTooComplex is returned when an instance requires more schema-application
// steps than maxStripSteps to strip. See docs/jsonschemautil.md ("Complexity limit").
var ErrTooComplex = errors.New("jsonschemautil: instance too complex to strip safely")

// maxStripSteps bounds the number of stripValue calls in a single
// StripReadOnly call.
const maxStripSteps = 100_000

// errStepBudgetExceeded unwinds the walk via panic/recover once maxStripSteps
// is exceeded.
var errStepBudgetExceeded = errors.New("jsonschemautil: step budget exceeded")

// patternCache memoizes regexp.Compile results for a single StripReadOnly
// call, keyed by the pattern string. A cached nil marks a pattern that
// failed to compile, so it's only attempted (and warned about) once.
type patternCache map[string]*regexp.Regexp

func (c patternCache) compile(pattern string) *regexp.Regexp {
	if re, ok := c[pattern]; ok {
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		slog.Warn("jsonschemautil: patternProperties pattern failed to compile, skipping",
			"pattern", pattern, "error", err)
		re = nil
	}
	c[pattern] = re
	return re
}

// stripWalker holds the state threaded through a single StripReadOnly call.
type stripWalker struct {
	root      *jsonschema.Schema
	cache     patternCache
	remaining int
}

func (w *stripWalker) step() {
	if w.remaining <= 0 {
		panic(errStepBudgetExceeded)
	}
	w.remaining--
}

// StripReadOnly parses rawSchema as a JSON Schema and deletes every field
// marked "readOnly": true from instance. See docs/jsonschemautil.md for the
// supported subset of JSON Schema.
//
// Callers must use the returned map, not the instance argument as passed
// in: for a non-nil instance both refer to the same, now-mutated map, but a
// nil instance returns a distinct new one - copy instance first if you
// still need the pre-strip data. A nil/empty rawSchema or a nil instance
// are each handled the same way ValidateInstance handles them (parse
// skipped; nil treated as {}).
//
// Returns ErrTooComplex, with instance possibly partially stripped, if the
// walk exceeds maxStripSteps - see docs/jsonschemautil.md ("Complexity limit").
func StripReadOnly(rawSchema json.RawMessage, instance map[string]any) (result map[string]any, err error) {
	if instance == nil {
		instance = map[string]any{}
	}
	if len(rawSchema) == 0 {
		return instance, nil
	}
	var sch jsonschema.Schema
	if err := json.Unmarshal(rawSchema, &sch); err != nil {
		return nil, fmt.Errorf("%w: parse schema: %w", ErrSchemaLoad, err)
	}

	w := &stripWalker{root: &sch, cache: patternCache{}, remaining: maxStripSteps}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if e, ok := r.(error); ok && errors.Is(e, errStepBudgetExceeded) {
			result, err = nil, fmt.Errorf("%w: exceeded %d schema-application steps", ErrTooComplex, maxStripSteps)
			return
		}
		panic(r)
	}()
	w.stripValue(&sch, instance)
	return instance, nil
}

// stripObject deletes from obj every key for which any applicable property
// schema is marked readOnly, then recurses into the surviving values with
// every applicable schema, so a nested readOnly field declared by any of
// them is stripped.
func (w *stripWalker) stripObject(sch *jsonschema.Schema, obj map[string]any) {
	if sch == nil || obj == nil {
		return
	}
	for name, value := range obj {
		propSchemas := propertySchemas(sch, name, w.cache)
		if slices.ContainsFunc(propSchemas, w.isReadOnly) {
			delete(obj, name)
			continue
		}
		for _, propSchema := range propSchemas {
			w.stripValue(propSchema, value)
		}
	}
}

// isReadOnly reports whether sch is readOnly, checking both the schema
// itself and, if present, what its "$ref" resolves to - a "readOnly"
// sibling of "$ref" and one declared on the ref target are both honored.
func (w *stripWalker) isReadOnly(sch *jsonschema.Schema) bool {
	if sch == nil {
		return false
	}
	if sch.ReadOnly {
		return true
	}
	resolved := w.resolveRef(sch)
	return resolved != nil && resolved.ReadOnly
}

// propertySchemas returns every (non-nil, not yet ref-resolved) schema that
// applies to instance property name across "properties", "patternProperties",
// and "additionalProperties" (see docs/jsonschemautil.md for precedence).
// $ref is resolved by the caller (isReadOnly, stripValue) rather than here,
// so a "readOnly" sibling of "$ref" isn't lost before it can be inspected.
func propertySchemas(sch *jsonschema.Schema, name string, cache patternCache) []*jsonschema.Schema {
	var matched []*jsonschema.Schema
	add := func(s *jsonschema.Schema) {
		if s != nil {
			matched = append(matched, s)
		}
	}

	evaluated := false
	if propSchema, ok := sch.Properties[name]; ok {
		evaluated = true
		add(propSchema)
	}
	for pattern, propSchema := range sch.PatternProperties {
		re := cache.compile(pattern)
		if re == nil {
			continue
		}
		if re.MatchString(name) {
			evaluated = true
			add(propSchema)
		}
	}
	if !evaluated {
		add(sch.AdditionalProperties)
	}
	return matched
}

// stripValue recurses into value if it's a JSON object or array and sch
// describes its shape; anything else (scalars, or no schema to recurse
// with) is left as-is.
func (w *stripWalker) stripValue(sch *jsonschema.Schema, value any) {
	if sch == nil {
		return
	}
	w.step()
	w.stripShape(sch, value)
	if sch.Ref != "" {
		if target := w.resolveRef(sch); target != nil {
			w.stripShape(target, value)
		}
	}
}

func (w *stripWalker) stripShape(sch *jsonschema.Schema, value any) {
	switch v := value.(type) {
	case map[string]any:
		w.stripObject(sch, v)
	case []any:
		for i, item := range v {
			itemSch := itemSchema(sch, i)
			if itemSch == nil {
				continue
			}
			w.stripValue(itemSch, item)
		}
	}
}

// itemSchema returns the schema for the array item at index; see
// docs/jsonschemautil.md for the prefixItems/items/legacy-tuple precedence.
func itemSchema(sch *jsonschema.Schema, index int) *jsonschema.Schema {
	if len(sch.PrefixItems) > 0 {
		if index < len(sch.PrefixItems) {
			return sch.PrefixItems[index]
		}
		return sch.Items
	}
	if len(sch.ItemsArray) > 0 {
		if index < len(sch.ItemsArray) {
			return sch.ItemsArray[index]
		}
		return sch.AdditionalItems
	}
	return sch.Items
}

// resolveRef follows a direct "#/$defs/<name>" or "#/definitions/<name>"
// $ref against w.root, one level; anything else is left unresolved (nil) -
// see docs/jsonschemautil.md for what's supported.
func (w *stripWalker) resolveRef(sch *jsonschema.Schema) *jsonschema.Schema {
	if sch == nil || sch.Ref == "" {
		return sch
	}
	name, ok := strings.CutPrefix(sch.Ref, "#/$defs/")
	if ok {
		return w.root.Defs[unescapeJSONPointerToken(name)]
	}
	if name, ok := strings.CutPrefix(sch.Ref, "#/definitions/"); ok {
		return w.root.Definitions[unescapeJSONPointerToken(name)]
	}
	return nil
}

// unescapeJSONPointerToken reverses the "~1"/"~0" escaping RFC 6901 requires
// for "/" and "~" within a single JSON Pointer token.
func unescapeJSONPointerToken(tok string) string {
	return strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
}
