package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPairTokenPersistenceAndAtomicRedemption(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	expires := time.Now().UTC().Add(10 * time.Minute)
	if err := d.CreatePairToken(ctx, "pair-hash", expires, ""); err != nil {
		t.Fatal(err)
	}
	p, err := d.PairTokenByHash(ctx, "pair-hash")
	if err != nil || p == nil || p.CreatedAt.IsZero() || p.UsedAt != nil || p.DeviceID != "" || p.ExpiresAt.Sub(expires).Abs() > time.Microsecond {
		t.Fatalf("row %+v %v", p, err)
	}
	if p, err := d.PairTokenByHash(ctx, "unknown"); err != nil || p != nil {
		t.Fatalf("missing %+v %v", p, err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			device := DeviceToken{ID: fmt.Sprintf("device-%d", i), Name: "小王的手机", Kind: "phone", Role: "full"}
			ok, err := d.RedeemPairToken(ctx, "pair-hash", device, fmt.Sprintf("device-hash-%d", i))
			results <- ok
			errors <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errors)
	success := 0
	for ok := range results {
		if ok {
			success++
		}
	}
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("redemptions %d", success)
	}
	devices, err := d.DeviceTokens(ctx)
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices %+v %v", devices, err)
	}
	p, err = d.PairTokenByHash(ctx, "pair-hash")
	if err != nil || p.UsedAt == nil || p.DeviceID != devices[0].ID {
		t.Fatalf("used row %+v %v", p, err)
	}
}

func TestPairTokenExpiredAndFailedInsert(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	device := DeviceToken{ID: "device", Name: "小王的手机", Kind: "phone", Role: "full"}
	if err := d.CreatePairToken(ctx, "expired", time.Now().Add(-time.Second), ""); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{"expired", "missing"} {
		if ok, err := d.RedeemPairToken(ctx, hash, device, "device-hash"); err != nil || ok {
			t.Fatalf("redeem %s = %v %v", hash, ok, err)
		}
	}
	if err := d.CreatePairToken(ctx, "unused", time.Now().Add(time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateDeviceToken(ctx, device, "existing-hash"); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.RedeemPairToken(ctx, "unused", device, "new-hash"); err == nil || ok {
		t.Fatal("duplicate device insert should roll back redemption")
	}
	p, err := d.PairTokenByHash(ctx, "unused")
	if err != nil || p.UsedAt != nil || p.DeviceID != "" {
		t.Fatalf("rollback row %+v %v", p, err)
	}
	device.ID = "another-device"
	if ok, err := d.RedeemPairToken(ctx, "unused", device, "new-hash"); err != nil || !ok {
		t.Fatalf("retry %v %v", ok, err)
	}
}

func TestRevokeDeviceTokenExpiresAssociatedPairToken(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	expires := time.Now().UTC().Add(10 * time.Minute)
	for _, hash := range []string{"revoked-pair", "other-pair", "unused-pair"} {
		if err := d.CreatePairToken(ctx, hash, expires, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"revoked", "other"} {
		device := DeviceToken{ID: id, Name: id, Kind: "phone", Role: "full"}
		if ok, err := d.RedeemPairToken(ctx, id+"-pair", device, id+"-hash"); err != nil || !ok {
			t.Fatalf("redeem %s = %v %v", id, ok, err)
		}
	}
	if ok, err := d.RevokeDeviceToken(ctx, "revoked"); err != nil || !ok {
		t.Fatalf("revoke = %v %v", ok, err)
	}
	p, err := d.PairTokenByHash(ctx, "revoked-pair")
	if err != nil || p == nil || p.UsedAt == nil || p.DeviceID != "revoked" {
		t.Fatalf("revoked pair %+v %v", p, err)
	}
	if p.ExpiresAt.After(time.Now()) {
		t.Fatalf("revoked device pairing token still expires in the future: %v", p.ExpiresAt)
	}
	for _, hash := range []string{"other-pair", "unused-pair"} {
		p, err := d.PairTokenByHash(ctx, hash)
		if err != nil || p == nil || p.ExpiresAt.Sub(expires).Abs() > time.Microsecond {
			t.Fatalf("unrelated pair %s changed: %+v %v", hash, p, err)
		}
	}
}

func TestRevokeLinkPairIsScopedAndIdempotent(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	expires := time.Now().Add(time.Hour)
	for _, hash := range []string{"candidate", "other"} {
		if err := d.CreatePairToken(ctx, hash, expires, "machine"); err != nil {
			t.Fatal(err)
		}
		device := DeviceToken{ID: hash + "-id", Name: hash, Kind: "link", Role: "link", MachineID: "machine"}
		if ok, err := d.RedeemPairToken(ctx, hash, device, hash+"-token"); err != nil || !ok {
			t.Fatalf("redeem %s: %v %v", hash, ok, err)
		}
	}
	if id, found, err := d.RevokeLinkPair(ctx, "candidate", "wrong-machine"); err != nil || found || id != "" {
		t.Fatalf("wrong machine: %q %v %v", id, found, err)
	}
	for i := 0; i < 2; i++ {
		if id, found, err := d.RevokeLinkPair(ctx, "candidate", "machine"); err != nil || !found || id != "candidate-id" {
			t.Fatalf("revoke %d: %q %v %v", i, id, found, err)
		}
	}
	if tok, err := d.DeviceTokenByHash(ctx, "candidate-token"); err != nil || tok != nil {
		t.Fatalf("candidate remains: %+v %v", tok, err)
	}
	if tok, err := d.DeviceTokenByHash(ctx, "other-token"); err != nil || tok == nil {
		t.Fatalf("other credential affected: %+v %v", tok, err)
	}
	if p, err := d.PairTokenByHash(ctx, "other"); err != nil || p == nil || p.ExpiresAt.Before(time.Now()) {
		t.Fatalf("other pair affected: %+v %v", p, err)
	}
	if err := d.CreatePairToken(ctx, "unused-candidate", expires, "machine"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if id, found, err := d.RevokeLinkPair(ctx, "unused-candidate", "machine"); err != nil || !found || id != "" {
			t.Fatalf("unused cancellation %d: %q %v %v", i, id, found, err)
		}
	}
	device := DeviceToken{ID: "late", Name: "late", Kind: "link", Role: "link", MachineID: "machine"}
	if ok, err := d.RedeemPairToken(ctx, "unused-candidate", device, "late-token"); err != nil || ok {
		t.Fatalf("redeemed cancelled code: %v %v", ok, err)
	}
	if _, _, err := d.RevokeLinkPair(ctx, "", "machine"); err == nil {
		t.Fatal("empty pair hash accepted")
	}
}

func TestRevokeLinkPairRejectsMismatchedCredential(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if err := d.CreatePairToken(ctx, "bad-pair", time.Now().Add(time.Hour), "machine"); err != nil {
		t.Fatal(err)
	}
	device := DeviceToken{ID: "other-id", Name: "other", Kind: "link", Role: "link", MachineID: "other-machine"}
	if ok, err := d.RedeemPairToken(ctx, "bad-pair", device, "other-token"); err != nil || !ok {
		t.Fatalf("redeem: %v %v", ok, err)
	}
	if _, _, err := d.RevokeLinkPair(ctx, "bad-pair", "machine"); err == nil {
		t.Fatal("mismatched credential accepted")
	}
	if tok, err := d.DeviceTokenByHash(ctx, "other-token"); err != nil || tok == nil {
		t.Fatalf("other credential affected: %+v %v", tok, err)
	}
	if p, err := d.PairTokenByHash(ctx, "bad-pair"); err != nil || p == nil || p.ExpiresAt.Before(time.Now()) {
		t.Fatalf("pair expiry was not rolled back: %+v %v", p, err)
	}
}

func TestRevokeLinkPairRacesRedemption(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		hash := fmt.Sprintf("race-pair-%d", i)
		id := fmt.Sprintf("race-id-%d", i)
		token := fmt.Sprintf("race-token-%d", i)
		if err := d.CreatePairToken(ctx, hash, time.Now().Add(time.Hour), "machine"); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var redeemed, found bool
		var revokedID string
		var redeemErr, revokeErr error
		go func() {
			defer wg.Done()
			<-start
			redeemed, redeemErr = d.RedeemPairToken(ctx, hash, DeviceToken{ID: id, Name: id, Kind: "link", Role: "link", MachineID: "machine"}, token)
		}()
		go func() {
			defer wg.Done()
			<-start
			revokedID, found, revokeErr = d.RevokeLinkPair(ctx, hash, "machine")
		}()
		close(start)
		wg.Wait()
		if redeemErr != nil || revokeErr != nil || !found {
			t.Fatalf("race %d: redeem=%v revoke=%v found=%v", i, redeemErr, revokeErr, found)
		}
		if redeemed && revokedID != id || !redeemed && revokedID != "" {
			t.Fatalf("race %d: redeemed=%v revoked=%q", i, redeemed, revokedID)
		}
		if tok, err := d.DeviceTokenByHash(ctx, token); err != nil || tok != nil {
			t.Fatalf("race %d left a live credential: %+v %v", i, tok, err)
		}
		if ok, err := d.RedeemPairToken(ctx, hash, DeviceToken{ID: id + "-late", Name: id, Kind: "link", Role: "link", MachineID: "machine"}, token+"-late"); err != nil || ok {
			t.Fatalf("race %d allowed late redemption: %v %v", i, ok, err)
		}
	}
}

// A pairing code made for one machine's persona-link carries the machine, and so does the credential it becomes.
func TestLinkPairTokenKeepsItsMachine(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if err := d.CreatePairToken(ctx, "link-pair", time.Now().Add(time.Minute), "laptop"); err != nil {
		t.Fatal(err)
	}
	p, err := d.PairTokenByHash(ctx, "link-pair")
	if err != nil || p == nil || p.MachineID != "laptop" {
		t.Fatalf("pair %+v %v", p, err)
	}
	device := DeviceToken{ID: "dev-link", Name: "笔记本 的 persona-link", Kind: "link", Role: "link", MachineID: "laptop"}
	if ok, err := d.RedeemPairToken(ctx, "link-pair", device, "link-hash"); !ok || err != nil {
		t.Fatalf("redeem %v %v", ok, err)
	}
	got, err := d.DeviceTokenByHash(ctx, "link-hash")
	if err != nil || got == nil || got.MachineID != "laptop" || got.Role != "link" {
		t.Fatalf("token %+v %v", got, err)
	}
	if err := d.CreateDeviceToken(ctx, DeviceToken{ID: "dev-phone2", Name: "手机", Kind: "phone", Role: "full"}, "phone-hash2"); err != nil {
		t.Fatal(err)
	}
	list, err := d.DeviceTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	machines := map[string]string{}
	for _, tok := range list {
		machines[tok.ID] = tok.MachineID
	}
	if machines["dev-link"] != "laptop" || machines["dev-phone2"] != "" {
		t.Fatalf("listed machines %v", machines)
	}
}
