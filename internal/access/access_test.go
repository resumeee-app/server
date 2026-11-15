package access_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/resumeee-app/resumeee-api/internal/access"
	"github.com/resumeee-app/resumeee-core"
)

type stubTemplateStore struct {
	core.TemplateStore
	tmpl *core.TemplateSet
	err  error
}

func (s *stubTemplateStore) Template(_ context.Context, _, _ string) (*core.TemplateSet, error) {
	return s.tmpl, s.err
}

type stubPresetStore struct {
	core.PresetStore
	preset *core.Preset
	byUser map[string]*core.Preset
	err    error
}

func (s *stubPresetStore) Preset(_ context.Context, userID, _ string) (*core.Preset, error) {
	if s.byUser != nil {
		if p, ok := s.byUser[userID]; ok {
			return p, nil
		}
		return nil, core.ErrNotFound
	}
	return s.preset, s.err
}

func TestStoreUnavailableError(t *testing.T) {
	err := access.NewStoreUnavailableError("template store unavailable")
	wrapped := fmt.Errorf("lookup failed: %w", err)
	msg, ok := access.StoreUnavailableMessage(wrapped)
	if !ok || msg != "template store unavailable" {
		t.Fatalf("got (%q, %v), want (%q, true)", msg, ok, "template store unavailable")
	}

	if _, ok := access.StoreUnavailableMessage(errors.New("other")); ok {
		t.Fatal("expected false for unrelated error")
	}

	var typedNil *access.StoreUnavailableError
	if msg, ok := access.StoreUnavailableMessage(fmt.Errorf("wrapped: %w", typedNil)); ok || msg != "" {
		t.Fatalf("expected (\"\", false) for wrapped typed-nil StoreUnavailableError, got (%q, %v)", msg, ok)
	}
}

func TestTemplateAuthorization(t *testing.T) {
	ctx := context.Background()

	if _, err := access.Template(ctx, nil, "user-1", "t-1"); err == nil {
		t.Fatal("expected unavailable error for nil store")
	} else if msg, ok := access.StoreUnavailableMessage(err); !ok || msg != "template store unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}

	otherTenant := &core.TemplateSet{ID: "builtin-spoofed", UserID: "user-2", IsBuiltIn: false}
	if _, err := access.Template(ctx, &stubTemplateStore{tmpl: otherTenant}, "user-1", otherTenant.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for other tenant's builtin-prefixed template, got %v", err)
	}

	builtIn := &core.TemplateSet{ID: "t-builtin", UserID: core.SystemUserID, IsBuiltIn: true}
	got, err := access.Template(ctx, &stubTemplateStore{tmpl: builtIn}, "user-1", builtIn.ID)
	if err != nil || got != builtIn {
		t.Fatalf("expected built-in template access, got (%v, %v)", got, err)
	}
}

func TestPresetAuthorization(t *testing.T) {
	ctx := context.Background()

	if _, err := access.Preset(ctx, nil, "user-1", "default", "p-1"); err == nil {
		t.Fatal("expected unavailable error for nil store")
	} else if msg, ok := access.StoreUnavailableMessage(err); !ok || msg != "preset store unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}

	otherTenant := &core.Preset{ID: "p-other", UserID: "user-2"}
	if _, err := access.Preset(ctx, &stubPresetStore{preset: otherTenant}, "user-1", "default", otherTenant.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for other tenant preset, got %v", err)
	}

	systemPreset := &core.Preset{ID: "p-sys", UserID: core.SystemUserID}
	got, err := access.Preset(ctx, &stubPresetStore{preset: systemPreset}, "user-1", "default", systemPreset.ID)
	if err != nil || got != systemPreset {
		t.Fatalf("expected system preset access, got (%v, %v)", got, err)
	}

	fallbackBuiltIn := &core.Preset{ID: "builtin-swe", UserID: "default"}
	fallbackStore := &stubPresetStore{byUser: map[string]*core.Preset{"default": fallbackBuiltIn}}
	gotFallback, err := access.Preset(ctx, fallbackStore, "user-1", "default", fallbackBuiltIn.ID)
	if err != nil || gotFallback != fallbackBuiltIn {
		t.Fatalf("expected fallback built-in preset access, got (%v, %v)", gotFallback, err)
	}

	fallbackPrivate := &core.Preset{ID: "p-private", UserID: "default"}
	fallbackPrivStore := &stubPresetStore{byUser: map[string]*core.Preset{"default": fallbackPrivate}}
	if _, err := access.Preset(ctx, fallbackPrivStore, "user-1", "default", fallbackPrivate.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-built-in fallback preset, got %v", err)
	}
}

func TestIsSystemAndFilterCustomSkills(t *testing.T) {
	if access.IsSystemTemplate(nil) || !access.IsSystemTemplate(&core.TemplateSet{ID: "builtin-clean"}) {
		t.Fatal("unexpected IsSystemTemplate result")
	}
	if access.IsSystemPreset(nil) || !access.IsSystemPreset(&core.Preset{ID: "builtin-ats"}) {
		t.Fatal("unexpected IsSystemPreset result")
	}

	skills := []core.Skill{
		{ID: "s1", UserID: "user-1", IsGlobal: false},
		{ID: "s2", UserID: "user-1", IsGlobal: true},
		{ID: "s3", UserID: "user-2", IsGlobal: false},
	}
	custom := access.FilterCustomSkills(skills, "user-1")
	if len(custom) != 1 || custom[0].ID != "s1" {
		t.Fatalf("unexpected custom skills: %+v", custom)
	}
}
