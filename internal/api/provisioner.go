package api

import (
	"context"
	"errors"
	"sync"

	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/seed"
)

// defaultTenantProvisioner lazily seeds default tenant data on first authenticated request.
type defaultTenantProvisioner struct {
	profileStore  ProfileStore
	branchStore   BranchStore
	templateStore TemplateStore
	userStore     UserStore
	provisioned   sync.Map
}

// Compile-time assertion ensuring defaultTenantProvisioner satisfies TenantProvisioner.
var _ TenantProvisioner = (*defaultTenantProvisioner)(nil)

// EnsureTenantProvisioned idempotently provisions default tenant records for userID if not already cached.
func (p *defaultTenantProvisioner) EnsureTenantProvisioned(ctx context.Context, userID string, userInfo *core.User) error {
	if _, ok := p.provisioned.Load(userID); ok {
		return nil
	}
	if p.profileStore == nil || p.branchStore == nil || p.templateStore == nil {
		return errors.New("tenant provisioner requires profile, branch, and template stores")
	}
	err := seed.EnsureTenantProvisionedStores(ctx, p.profileStore, p.branchStore, p.templateStore, p.userStore, userID, userInfo)
	if err == nil {
		p.provisioned.Store(userID, true)
	}
	return err
}
