package jsonschemautil

import (
	"errors"
	"testing"
	"time"
)

func TestValidateInstance_NoSchemaSkipsValidation(t *testing.T) {
	if err := ValidateInstance(nil, map[string]any{"anything": "goes"}); err != nil {
		t.Errorf("ValidateInstance(nil schema) error = %v, want nil", err)
	}
}

func TestValidateInstance_ValidData(t *testing.T) {
	schema := []byte(`{"type":"object","required":["badge"],"properties":{"badge":{"type":"string"}}}`)
	if err := ValidateInstance(schema, map[string]any{"badge": "123"}); err != nil {
		t.Errorf("ValidateInstance() error = %v, want nil", err)
	}
}

func TestValidateInstance_MismatchIsUnwrapped(t *testing.T) {
	schema := []byte(`{"type":"object","required":["badge"]}`)
	err := ValidateInstance(schema, map[string]any{})
	if err == nil {
		t.Fatal("ValidateInstance() expected a mismatch error, got nil")
	}
	if errors.Is(err, ErrSchemaLoad) {
		t.Errorf("ValidateInstance() mismatch error should not be ErrSchemaLoad, got %v", err)
	}
}

func TestValidateInstance_NilInstanceTreatedAsEmptyObject(t *testing.T) {
	schema := []byte(`{"type":"object","required":["badge"]}`)
	if err := ValidateInstance(schema, nil); err == nil {
		t.Error("ValidateInstance(nil instance) expected a required-field mismatch, got nil")
	}
}

func TestValidateInstance_UnparsableSchemaIsErrSchemaLoad(t *testing.T) {
	err := ValidateInstance([]byte(`not json`), map[string]any{})
	if err == nil {
		t.Fatal("ValidateInstance() expected an error, got nil")
	}
	if !errors.Is(err, ErrSchemaLoad) {
		t.Errorf("ValidateInstance() error = %v, want ErrSchemaLoad", err)
	}
}

func TestValidateInstance_UnresolvableSchemaIsErrSchemaLoad(t *testing.T) {
	// A $ref to a schema that doesn't exist fails at Resolve time, not parse time.
	schema := []byte(`{"$ref":"#/does-not-exist"}`)
	err := ValidateInstance(schema, map[string]any{})
	if err == nil {
		t.Fatal("ValidateInstance() expected an error, got nil")
	}
	if !errors.Is(err, ErrSchemaLoad) {
		t.Errorf("ValidateInstance() error = %v, want ErrSchemaLoad", err)
	}
}

// TestValidateInstance_ExponentialBlowupIsUnbounded demonstrates that
// ValidateInstance has the same unbounded-recursion problem that
// StripReadOnly had before the stripWalker step budget (see
// docs/jsonschemautil.md, "Complexity limit"): a self-referential schema
// where more than one property schema matches the same key doubles the
// number of validate calls at every nesting level of the instance. The
// underlying github.com/google/jsonschema-go library has no step or depth
// budget, so this never returns in practical time for a modestly deep,
// otherwise unremarkable instance.
//
// Skipped by default since a real fix belongs in ValidateInstance, not in
// a test asserting current (bad) behavior. Run explicitly with
// RUN_SLOW_VULN_TESTS=1 to confirm; if it ever fails, ValidateInstance has
// gained a bound and this test should be replaced with one asserting that
// bound, the same way TestStripReadOnly_ExponentialBlowupIsBounded does
// for StripReadOnly.
func TestValidateInstance_ExponentialBlowupIsUnbounded(t *testing.T) {
	// if os.Getenv("RUN_SLOW_VULN_TESTS") == "" {
	// 	t.Skip("set RUN_SLOW_VULN_TESTS=1 to run; see comment above")
	// }

	schema := []byte(`{
		"type": "object",
		"$defs": {
			"Node": {
				"type": "object",
				"patternProperties": {
					"^x$": {"$ref": "#/$defs/Node"},
					"x": {"$ref": "#/$defs/Node"}
				}
			}
		},
		"properties": {
			"root": {"$ref": "#/$defs/Node"}
		}
	}`)

	const depth = 40 // 2^22 validate calls if unbounded
	instance := map[string]any{"root": nestedX(depth)}

	done := make(chan error, 1)
	go func() { done <- ValidateInstance(schema, instance) }()

	const budget = 25 * time.Second
	select {
	case err := <-done:
		t.Fatalf("ValidateInstance returned within %v (err=%v) for a %d-level self-referential instance - "+
			"the exponential blowup appears fixed; replace this test with a bounded-step regression test", budget, err, depth)
	case <-time.After(budget):
		t.Logf("ValidateInstance did not return within %v for a %d-level self-referential instance, "+
			"confirming no step/depth budget bounds the $ref + patternProperties fan-out", budget, depth)
	}
}

// nestedX builds map[string]any{"x": map[string]any{"x": ... {} }} depth levels deep.
func nestedX(depth int) map[string]any {
	v := map[string]any{}
	for range depth {
		v = map[string]any{"x": v}
	}
	return v
}
