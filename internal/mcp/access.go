package mcp

import (
	"context"
	"errors"

	"github.com/resumeee-app/resumeee-core"
)

// storeUnavailableError indicates a required domain store was omitted from ServerConfig.
type storeUnavailableError struct {
	msg string
}

// Error returns the store unavailability message.
func (e *storeUnavailableError) Error() string {
	return e.msg
}

// unavailableErrorMessage extracts the store unavailability message if err wraps *storeUnavailableError.
func unavailableErrorMessage(err error) (string, bool) {
	var unavailable *storeUnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.Error(), true
	}
	return "", false
}

// fetchAccessibleTemplate resolves templateID from store and verifies tenant ownership or built-in visibility.
func fetchAccessibleTemplate(ctx context.Context, store core.TemplateStore, userID, templateID string) (*core.TemplateSet, error) {
	if store == nil {
		return nil, &storeUnavailableError{msg: "template store unavailable"}
	}
	tmpl, err := store.Template(ctx, userID, templateID)
	if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn && tmpl.UserID != core.SystemUserID) {
		return nil, core.ErrForbidden
	}
	return tmpl, nil
}

// fetchAccessiblePreset resolves presetID from store, checking user ownership and built-in/system defaults.
func fetchAccessiblePreset(ctx context.Context, store core.PresetStore, userID, fallbackUserID, presetID string) (*core.Preset, error) {
	if store == nil {
		return nil, &storeUnavailableError{msg: "preset store unavailable"}
	}
	preset, err := store.Preset(ctx, userID, presetID)
	if (err != nil || preset == nil) && fallbackUserID != "" && fallbackUserID != userID {
		if p, pErr := store.Preset(ctx, fallbackUserID, presetID); pErr == nil && p != nil {
			preset = p
			err = nil
		}
	}
	if err != nil || preset == nil || (preset.UserID != userID && !preset.IsBuiltIn() && preset.UserID != core.SystemUserID) {
		return nil, core.ErrForbidden
	}
	return preset, nil
}
