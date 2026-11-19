package mail

import (
	"context"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// IntakeMailboxes returns the inbox and ordinary selectable mailboxes, including archive folders. LIST supplies
// their names and roles; collection does not need a separate STATUS request for every folder on older servers.
func (s *Service) IntakeMailboxes(ctx context.Context, accountID string) ([]string, error) {
	a, err := s.account(accountID)
	if err != nil {
		return nil, err
	}
	var folders []string
	err = s.withConn(ctx, a, func(c *imapclient.Client) error {
		rows, err := c.List("", "*", listOptions(c.Caps(), nil)).Collect()
		if err != nil {
			return err
		}
		folders = []string{"INBOX"} // IMAP always has an INBOX, even if LIST omitted it.
		for _, row := range rows {
			if strings.EqualFold(row.Mailbox, "INBOX") || !intakeMailbox(row) {
				continue
			}
			folders = append(folders, row.Mailbox)
		}
		slices.Sort(folders[1:])
		folders = slices.Compact(folders)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return folders, nil
}

func intakeMailbox(row *imap.ListData) bool {
	for _, attr := range row.Attrs {
		switch attr {
		case imap.MailboxAttrNoSelect, imap.MailboxAttrNonExistent, imap.MailboxAttrAll, imap.MailboxAttrFlagged,
			imap.MailboxAttrImportant, imap.MailboxAttrSent, imap.MailboxAttrDrafts, imap.MailboxAttrTrash, imap.MailboxAttrJunk:
			return false
		}
	}
	if hasAttr(row.Attrs, imap.MailboxAttrArchive) {
		return true
	}
	name := strings.ToLower(row.Mailbox)
	if bare := strings.TrimPrefix(strings.TrimPrefix(name, "[gmail]/"), "[google mail]/"); bare != name {
		name = bare
		switch name {
		case "all mail", "starred", "important":
			return false
		}
	}
	switch name { // Common names on servers without SPECIAL-USE, including QQ Mail.
	// i18n-ignore: match mailbox names returned by IMAP servers regardless of the user's display language.
	case "已发送", "已发送邮件", "草稿", "草稿箱", "已删除", "已删除邮件", "垃圾箱", "垃圾邮件":
		return false
	}
	switch roleOf(name, row.Attrs) {
	case RoleSent, RoleDrafts, RoleTrash, RoleJunk:
		return false
	}
	return true
}
