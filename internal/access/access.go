// Package access provides shared tenant ownership, system-resource, and store availability helpers.
package access

import (
	"context"
	"errors"
	"strings"

	"github.com/resumeee-app/resumeee-core"
)

// StoreUnavailableError indicates a required domain store was omitted from handler configuration.
type StoreUnavailableError struct {
	msg string
}

// NewStoreUnavailableError constructs a StoreUnavailableError with the given diagnostic message.
func NewStoreUnavailableError(msg string) *StoreUnavailableError {
	return &StoreUnavailableError{msg: msg}
}

// Error returns the store unavailability diagnostic message.
func (e *StoreUnavailableError) Error() string {
	if e == nil {
		return ""
	}
	return e.msg
}

// StoreUnavailableMessage extracts the unavailability message if err wraps *StoreUnavailableError.
func StoreUnavailableMessage(err error) (string, bool) {
	var unavailable *StoreUnavailableError
	if errors.As(err, &unavailable) && unavailable != nil {
		return unavailable.msg, true
	}
	return "", false
}

// IsSystemTemplate reports whether tmpl represents a built-in or system-owned template set.
func IsSystemTemplate(tmpl *core.TemplateSet) bool {
	return tmpl != nil && (tmpl.IsBuiltIn || strings.HasPrefix(tmpl.ID, "builtin-") || tmpl.UserID == core.SystemUserID)
}

// IsSystemPreset reports whether preset represents a built-in or system-owned tailoring strategy.
func IsSystemPreset(preset *core.Preset) bool {
	return preset != nil && (preset.IsBuiltIn() || strings.HasPrefix(preset.ID, "builtin-") || preset.UserID == core.SystemUserID)
}

// Template resolves templateID from store and verifies tenant ownership or built-in/system visibility.
func Template(ctx context.Context, store core.TemplateStore, userID, templateID string) (*core.TemplateSet, error) {
	if store == nil {
		return nil, NewStoreUnavailableError("template store unavailable")
	}
	tmpl, err := store.Template(ctx, userID, templateID)
	if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn && tmpl.UserID != core.SystemUserID) {
		return nil, core.ErrForbidden
	}
	return tmpl, nil
}

// Preset resolves presetID from store, checking user ownership, fallback user built-in visibility, and system defaults.
func Preset(ctx context.Context, store core.PresetStore, userID, fallbackUserID, presetID string) (*core.Preset, error) {
	if store == nil {
		return nil, NewStoreUnavailableError("preset store unavailable")
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

// FilterCustomSkills returns the subset of skills that belong directly to userID and are not global catalog entries.
func FilterCustomSkills(skills []core.Skill, userID string) []core.Skill {
	filtered := make([]core.Skill, 0, len(skills))
	for _, s := range skills {
		if !s.IsGlobal && s.UserID == userID {
			filtered = append(filtered, s)
		}
	}
	return filtered
}
