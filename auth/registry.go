package auth

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/resumeee-app/core"
)

// Config provides configuration parameters required to initialize an auth.Registry.
type Config struct {
	DefaultUserID string
	AuthProvider  string
	Admin         bool
}

// RegistryOption configures an auth.Registry during initialization in NewRegistryFromConfig.
type RegistryOption func(*Registry) error

// WithProvider injects an HTTP-aware auth.Provider into the registry.
func WithProvider(provider Provider) RegistryOption {
	return func(r *Registry) error {
		if provider == nil {
			return errors.New("auth: provider cannot be nil")
		}
		if strings.TrimSpace(provider.Name()) == "" {
			return errors.New("auth: provider name cannot be empty")
		}
		r.Register(provider)
		return nil
	}
}

// WithAuthProvider injects a core.AuthProvider into the registry, adapting it for HTTP Bearer token extraction.
func WithAuthProvider(provider core.AuthProvider) RegistryOption {
	return func(r *Registry) error {
		if provider == nil {
			return errors.New("auth: AuthProvider cannot be nil")
		}
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			return errors.New("auth: AuthProvider name cannot be empty")
		}
		if strings.EqualFold(name, ProviderAdmin) {
			return errors.New("auth: admin AuthProvider requires Admin mode or WithProvider")
		}
		r.RegisterVerifier(provider)
		return nil
	}
}

// WithTokenVerifier injects a core.TokenVerifier into the registry under the specified provider name.
func WithTokenVerifier(name string, verifier core.TokenVerifier) RegistryOption {
	return func(r *Registry) error {
		if verifier == nil {
			return errors.New("auth: TokenVerifier cannot be nil")
		}
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return errors.New("auth: TokenVerifier name cannot be empty")
		}
		if strings.EqualFold(trimmed, ProviderAdmin) {
			return errors.New("auth: provider name \"admin\" is reserved for AdminProvider")
		}
		r.Register(NewTokenVerifierProvider(trimmed, verifier))
		return nil
	}
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

// NewRegistryFromConfig initializes an auth.Registry from runtime configuration and injected provider options.
func NewRegistryFromConfig(cfg Config, opts ...RegistryOption) (*Registry, error) {
	r := NewRegistry()

	defaultUserID := cfg.DefaultUserID
	if defaultUserID == "" {
		defaultUserID = fallbackUserID
	}

	// Always register Admin provider so explicit selection remains available.
	adminProvider := NewAdminProvider(defaultUserID)
	r.Register(adminProvider)

	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(r); err != nil {
			return nil, err
		}
	}

	// Reset defaultProvider under mutex so explicit selection hierarchy determines the active default.
	r.ClearDefault()

	injected := r.nonAdminNames()

	// Resolve default provider hierarchy.
	selected := strings.ToLower(strings.TrimSpace(cfg.AuthProvider))
	switch {
	case selected != "":
		if err := r.SetDefault(selected); err != nil {
			return r, err
		}
	case cfg.Admin:
		_ = r.SetDefault(ProviderAdmin)
	case len(injected) == 1:
		_ = r.SetDefault(injected[0])
	default:
		// When neither explicit AuthProvider, Admin, nor a single injected vendor provider is configured,
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
	if name == "" || name == ProviderAdmin {
		return
	}
	r.Register(NewTokenVerifierProvider(name, provider))
}

// Provider retrieves a registered provider by name (case-insensitive).
func (r *Registry) Provider(name string) (Provider, error) {
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

// Get retrieves a registered provider by name (case-insensitive).
//
// Deprecated: Use [Registry.Provider] instead.
func (r *Registry) Get(name string) (Provider, error) {
	return r.Provider(name)
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
	slices.Sort(names)
	return names
}

// nonAdminNames returns sorted registered provider names excluding the built-in admin provider.
func (r *Registry) nonAdminNames() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for name := range r.providers {
		if name != ProviderAdmin {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
