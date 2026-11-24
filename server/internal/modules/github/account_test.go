package github

import (
	"context"
	"sync"
	"testing"
)

// signedInAs puts token in memory as one GitHub has named the account of already, for a fake GitHub that serves
// no /user: what is read as an account (the thread cache, the inbox) asks whose a new token is first.
func signedInAs(c *Connector, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.newCredential(token)
	c.acct.login, c.acct.known = "example-user", true
}

// Sign-out while reads run: once Logout has returned and the reads have ended, no token is in memory. (Logout
// drops the token in memory before the one kept for the device flow, and a read in between puts it back.)
func TestLogoutLeavesNoToken(t *testing.T) {
	f := newTwoAccounts(t)
	d := &deviceWeb{}
	c := f.deviceConnector(t, d, tokenA)
	ctx := context.Background()
	for i := range 300 {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						_, _, _ = c.tokenNow(ctx, "")
					}
				}
			}()
		}
		if err := c.Logout(ctx); err != nil {
			t.Fatal(err)
		}
		close(stop)
		wg.Wait()
		c.mu.Lock()
		tok := c.token
		c.mu.Unlock()
		if tok != "" {
			c.check(ctx)
			t.Fatalf("round %d: signed out, and the token is back in memory; status %+v", i, c.Status(ctx))
		}
		signInDevice(t, c, d, tokenA)
	}
}
