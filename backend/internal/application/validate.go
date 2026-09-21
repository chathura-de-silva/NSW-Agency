package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/OpenNSW/agency/backend/pkg/jsonschemautil"
	"github.com/OpenNSW/core/artifact"
	"github.com/OpenNSW/core/artifact/adapter/generictemplate"
)

// validateAgainstFormSchema validates data against the JSON Schema embedded
// in the form artifact identified by formID — a task's view form (injected
// data) or review form (reviewer response). kind names the form in error
// messages ("view", "review"); wrapErr is the sentinel a schema mismatch is
// wrapped in, so each caller can map it to its own 400 the way
// ErrInvalidInjectRequest already is. A load, parse, or resolve failure is
// returned unwrapped so the caller treats it as an internal/config error
// (fail closed on config drift); only a schema mismatch reflects bad caller
// data.
func validateAgainstFormSchema(ctx context.Context, reg *artifact.Registry, kind, formID string, data map[string]any, wrapErr error) error {
	raw, err := generictemplate.Load(ctx, reg, formID)
	if err != nil {
		return fmt.Errorf("failed to load %s form %q: %w", kind, formID, err)
	}

	var form struct {
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(raw, &form); err != nil {
		return fmt.Errorf("failed to parse %s form %q: %w", kind, formID, err)
	}

	if err := jsonschemautil.ValidateInstance(form.Schema, data); err != nil {
		if errors.Is(err, jsonschemautil.ErrSchemaLoad) {
			return fmt.Errorf("failed to load %s form %q schema: %w", kind, formID, err)
		}
		return fmt.Errorf("%w: data does not match %s form %q schema: %v", wrapErr, kind, formID, err)
	}
	return nil
}
