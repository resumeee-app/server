package auth

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	firebase "github.com/resumeee-app/resumeee-auth-firebase"
	"github.com/resumeee-app/resumeee-core"
)

// Config provides configuration parameters required to initialize an auth.Registry.
type Config struct {
	DefaultUserID     string
	FirebaseProjectID string
	AuthProvider      string
	Admin             bool
}

// Registry manages registered authentication providers and resolves active selection.
type Registry struct {
	mu              sync.RWMutex
	providers       map[string]Provider
	defaultProvider string
}

// NewRegistry constructs an empty auth.Registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// NewRegistryFromConfig initializes an auth.Registry from runtime configuration.
func NewRegistryFromConfig(cfg Config, opts ...firebase.FirebaseTokenVerifierOption) (*Registry, error) {
	r := NewRegistry()

	defaultUserID := cfg.DefaultUserID
	if defaultUserID == "" {
		defaultUserID = fallbackUserID
	}

	// Always register Admin provider
	adminProvider := NewAdminProvider(defaultUserID)
	r.Register(adminProvider)

	// Register Firebase provider if project ID is provided
	if cfg.FirebaseProjectID != "" {
		firebaseProvider := firebase.NewFirebaseTokenVerifier(cfg.FirebaseProjectID, opts...)
		r.RegisterVerifier(firebaseProvider)
	}

	// Reset defaultProvider under mutex so explicit selection hierarchy determines the active default
	r.ClearDefault()

	// Resolve default provider hierarchy
	selected := strings.ToLower(strings.TrimSpace(cfg.AuthProvider))
	switch {
	case selected != "":
		if err := r.SetDefault(selected); err != nil {
			return r, err
		}
	case cfg.Admin:
		_ = r.SetDefault(ProviderAdmin)
	case cfg.FirebaseProjectID != "":
		_ = r.SetDefault(ProviderFirebase)
	default:
		// When neither explicit AuthProvider, Admin, nor FirebaseProjectID is configured,
		// leave defaultProvider empty so callers fail closed in multi-tenant mode.
	}

	return r, nil
}

// Register adds an HTTP-aware Provider to the registry under its normalized lowercase name.
func (r *Registry) Register(provider Provider) {
	if r == nil || provider == nil {
		return
	}
	name := strings.ToLower(strings.TrimSpace(provider.Name()))
	if name == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.providers[name] = provider
	if r.defaultProvider == "" {
		r.defaultProvider = name
	}
}

// RegisterVerifier adapts a bearer-only core.AuthProvider via NewTokenVerifierProvider and registers it.
func (r *Registry) RegisterVerifier(provider core.AuthProvider) {
	if r == nil || provider == nil {
		return
	}
	name := strings.ToLower(strings.TrimSpace(provider.Name()))
	if name == "" {
		return
	}
	r.Register(NewTokenVerifierProvider(name, provider))
}

// Get retrieves a registered provider by name (case-insensitive).
func (r *Registry) Get(name string) (Provider, error) {
	if r == nil {
		return nil, errors.New("nil auth registry")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	norm := strings.ToLower(strings.TrimSpace(name))
	if norm == "" {
		return nil, errors.New("empty auth provider name")
	}
	p, ok := r.providers[norm]
	if !ok {
		return nil, fmt.Errorf("auth provider %q not registered", name)
	}
	return p, nil
}

// Default returns the default auth provider.
func (r *Registry) Default() (Provider, error) {
	if r == nil {
		return nil, errors.New("nil auth registry")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.defaultProvider == "" {
		return nil, errors.New("no default auth provider configured")
	}
	p, ok := r.providers[r.defaultProvider]
	if !ok {
		return nil, fmt.Errorf("default auth provider %q not registered", r.defaultProvider)
	}
	return p, nil
}

// SetDefault designates the default auth provider name.
func (r *Registry) SetDefault(name string) error {
	if r == nil {
		return errors.New("nil auth registry")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	norm := strings.ToLower(strings.TrimSpace(name))
	if norm == "" {
		return errors.New("empty default provider name")
	}
	if _, ok := r.providers[norm]; !ok {
		return fmt.Errorf("cannot set default: auth provider %q not registered", name)
	}
	r.defaultProvider = norm
	return nil
}

// ClearDefault clears the designated default provider.
func (r *Registry) ClearDefault() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultProvider = ""
}

// List returns a sorted list of registered provider names.
func (r *Registry) List() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
