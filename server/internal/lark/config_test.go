package lark

import (
	"testing"

	"github.com/nwverse/persona/server/internal/extwire"
)

// extwire is a contract and may not import this package, so it keeps its own copy of the account limit for the avatar
// links it checks (#11793). The two must stay equal: a larger account here would get links the hub refuses.
func TestMaxAccountMatchesTheContract(t *testing.T) {
	if MaxAccount != extwire.LarkMaxAccount {
		t.Errorf("lark.MaxAccount = %d, extwire.LarkMaxAccount = %d: change both together", MaxAccount, extwire.LarkMaxAccount)
	}
}
