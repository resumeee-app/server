package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-core"
)

type stubVerifier struct {
	uid string
	err error
}

func (s *stubVerifier) VerifyIDToken(_ context.Context, _ string) (*core.TokenInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &core.TokenInfo{UID: s.uid}, nil
}

func TestExtractBearerToken(t *testing.T) {
	if _, err := auth.ExtractBearerToken(nil); !errors.Is(err, auth.ErrMissingBearerHeader) {
		t.Fatalf("expected ErrMissingBearerHeader for nil request, got %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := auth.ExtractBearerToken(req); !errors.Is(err, auth.ErrMissingBearerHeader) {
		t.Fatalf("expected ErrMissingBearerHeader for missing header, got %v", err)
	}

	req.Header.Set("Authorization", "Bearer   ")
	if _, err := auth.ExtractBearerToken(req); !errors.Is(err, auth.ErrEmptyBearerToken) {
		t.Fatalf("expected ErrEmptyBearerToken, got %v", err)
	}

	req.Header.Set("Authorization", "Bearer token-123")
	tok, err := auth.ExtractBearerToken(req)
	if err != nil || tok != "token-123" {
		t.Fatalf("expected token-123, got %q (err=%v)", tok, err)
	}
}

func TestAdminProvider(t *testing.T) {
	p := auth.NewAdminProvider("default", auth.WithDefaultUserID("admin-user"))
	if p.DefaultUserID() != "admin-user" {
		t.Fatalf("expected DefaultUserID 'admin-user', got %q", p.DefaultUserID())
	}
	if p.Name() != auth.ProviderAdmin {
		t.Fatalf("expected Name 'admin', got %q", p.Name())
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	info, err := p.AuthenticateRequest(req)
	if err != nil || info.UID != "admin-user" || !info.IsAdmin() {
		t.Fatalf("unexpected AuthenticateRequest result: %+v, err=%v", info, err)
	}

	req.Header.Set("X-User-ID", "  override-uid  ")
	info, err = p.AuthenticateRequest(req)
	if err != nil || info.UID != "override-uid" {
		t.Fatalf("expected override-uid, got %+v, err=%v", info, err)
	}

	info, err = p.VerifyIDToken(context.Background(), "admin-token-token")
	if err != nil || info.UID != "admin-token-token" || !info.IsAdmin() {
		t.Fatalf("expected admin-token-token, got %+v, err=%v", info, err)
	}

	info, err = p.VerifyIDToken(context.Background(), "   ")
	if err != nil || info.UID != "admin-user" || !info.IsAdmin() {
		t.Fatalf("expected fallback admin-user, got %+v, err=%v", info, err)
	}
}

type mockEmptyNameProvider struct{}

func (m *mockEmptyNameProvider) Name() string { return "   " }
func (m *mockEmptyNameProvider) AuthenticateRequest(_ *http.Request) (*core.TokenInfo, error) {
	return nil, nil
}
func (m *mockEmptyNameProvider) VerifyIDToken(_ context.Context, _ string) (*core.TokenInfo, error) {
	return nil, nil
}

func TestTokenVerifierProviderAndRegistry(t *testing.T) {
	tvp := auth.NewTokenVerifierProvider("custom", &stubVerifier{uid: "u-1"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer valid-tok")
	info, err := tvp.AuthenticateRequest(req)
	if err != nil || info.UID != "u-1" {
		t.Fatalf("expected u-1, got %+v, err=%v", info, err)
	}

	reg := auth.NewRegistry()
	if _, err := reg.Provider("unknown"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not registered error, got %v", err)
	}
	if _, err := reg.Default(); err == nil || !strings.Contains(err.Error(), "no default auth provider") {
		t.Fatalf("expected no default auth provider error, got %v", err)
	}

	adminProv := auth.NewAdminProvider("default")
	reg.Register(adminProv)
	reg.Register(tvp)

	if err := reg.SetDefault("custom"); err != nil {
		t.Fatalf("unexpected error setting default: %v", err)
	}
	def, err := reg.Default()
	if err != nil || def.Name() != "custom" {
		t.Fatalf("expected default provider 'custom', got %v, err=%v", def, err)
	}
	if !reflect.DeepEqual(reg.List(), []string{auth.ProviderAdmin, "custom"}) {
		t.Fatalf("unexpected list: %v", reg.List())
	}
}

func TestNewRegistryFromConfig(t *testing.T) {
	t.Run("admin mode enabled initializes admin provider as default", func(t *testing.T) {
		cfg := auth.Config{
			Admin:         true,
			DefaultUserID: "super-admin",
		}
		r, err := auth.NewRegistryFromConfig(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		def, err := r.Default()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if def.Name() != auth.ProviderAdmin {
			t.Errorf("expected default %q, got %q", auth.ProviderAdmin, def.Name())
		}
	})

	t.Run("firebase project id initializes firebase provider as default", func(t *testing.T) {
		cfg := auth.Config{
			FirebaseProjectID: "demo-project",
		}
		r, err := auth.NewRegistryFromConfig(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		def, err := r.Default()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if def.Name() != auth.ProviderFirebase {
			t.Errorf("expected default %q, got %q", auth.ProviderFirebase, def.Name())
		}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
		if _, err := def.AuthenticateRequest(req); !errors.Is(err, auth.ErrMissingBearerHeader) {
			t.Errorf("expected ErrMissingBearerHeader from wrapped firebase provider, got %v", err)
		}
	})

	t.Run("explicit auth provider selects default", func(t *testing.T) {
		cfg := auth.Config{
			AuthProvider:      auth.ProviderAdmin,
			FirebaseProjectID: "demo-project",
			DefaultUserID:     "admin-user",
		}
		r, err := auth.NewRegistryFromConfig(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		def, err := r.Default()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if def.Name() != auth.ProviderAdmin {
			t.Errorf("expected default %q, got %q", auth.ProviderAdmin, def.Name())
		}
	})
}

type verifierOnlyProvider struct {
	name string
	uid  string
}

func (v *verifierOnlyProvider) Name() string { return v.name }
func (v *verifierOnlyProvider) VerifyIDToken(_ context.Context, _ string) (*core.TokenInfo, error) {
	return &core.TokenInfo{UID: v.uid}, nil
}

func TestDefensiveNilReceivers(t *testing.T) {
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)

	var r *auth.Registry
	r.Register(nil)
	r.RegisterVerifier(nil)
	r.ClearDefault()
	if r.List() != nil {
		t.Errorf("expected nil list from nil registry")
	}
	if _, err := r.Provider("admin"); err == nil {
		t.Error("expected error from nil registry Provider")
	}
	if _, err := r.Default(); err == nil {
		t.Error("expected error from nil registry Default")
	}
	if err := r.SetDefault("admin"); err == nil {
		t.Error("expected error from nil registry SetDefault")
	}

	var p *auth.AdminProvider
	if p.Name() != auth.ProviderAdmin || p.DefaultUserID() != "default" {
		t.Errorf("unexpected nil AdminProvider defaults")
	}
	if info, err := p.AuthenticateRequest(req); err != nil || info.UID != "default" {
		t.Errorf("unexpected nil AdminProvider AuthenticateRequest: %+v, %v", info, err)
	}
	if info, err := p.VerifyIDToken(ctx, ""); err != nil || info.UID != "default" {
		t.Errorf("unexpected nil AdminProvider VerifyIDToken: %+v, %v", info, err)
	}

	var tvp *auth.TokenVerifierProvider
	if tvp.Name() != "custom" {
		t.Errorf("expected 'custom', got %q", tvp.Name())
	}
	if _, err := tvp.AuthenticateRequest(req); !errors.Is(err, auth.ErrProviderNotConfigured) {
		t.Errorf("expected ErrProviderNotConfigured, got %v", err)
	}
	if _, err := tvp.VerifyIDToken(ctx, "token"); !errors.Is(err, core.ErrUnauthorized) {
		t.Errorf("expected core.ErrUnauthorized, got %v", err)
	}

	reg := auth.NewRegistry()
	reg.Register(&mockEmptyNameProvider{})
	reg.RegisterVerifier(&verifierOnlyProvider{name: "  ", uid: "ignored"})
	if len(reg.List()) != 0 {
		t.Errorf("expected empty registry")
	}

	reg.RegisterVerifier(auth.NewAdminProvider("default"))
	if len(reg.List()) != 0 {
		t.Errorf("expected RegisterVerifier to ignore reserved admin provider")
	}

	reg.RegisterVerifier(&verifierOnlyProvider{name: "custom-jwt", uid: "jwt-user"})
	prov, err := reg.Provider("custom-jwt")
	if err != nil {
		t.Fatalf("expected custom-jwt to be registered via RegisterVerifier: %v", err)
	}
	aliasProv, err := reg.Get("custom-jwt") //nolint:staticcheck // verify deprecated alias delegates to Provider
	if err != nil || aliasProv != prov {
		t.Fatalf("expected deprecated Get alias to return matching provider, got %v (err=%v)", aliasProv, err)
	}
	bearerReq := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
	bearerReq.Header.Set("Authorization", "Bearer valid")
	info, err := prov.AuthenticateRequest(bearerReq)
	if err != nil || info.UID != "jwt-user" {
		t.Fatalf("expected jwt-user from adapted verifier, got %+v (err=%v)", info, err)
	}
}
