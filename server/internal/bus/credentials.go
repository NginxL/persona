package bus

import (
	"context"
	"time"
)

const (
	// EventCredentialRevoked tells managers which device credential was signed out.
	EventCredentialRevoked = "credential.revoked"
	// MethodCredentialRevoked checks a queued message's sender even if its manager missed the event.
	MethodCredentialRevoked = "credential.is_revoked"
)

// DeviceCredential identifies a device credential by its hub-issued ID.
type DeviceCredential struct {
	ID string `json:"credential_id"`
}

// CredentialStatus is the hub's answer about a device credential.
type CredentialStatus struct {
	Revoked bool `json:"revoked"`
}

// CredentialRevoked checks the hub before a manager sends a queued message with device provenance.
func CredentialRevoked(ctx context.Context, call func(context.Context, string, any, any) error, id string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var status CredentialStatus
	err := call(ctx, MethodCredentialRevoked, DeviceCredential{ID: id}, &status)
	return status.Revoked, err
}
