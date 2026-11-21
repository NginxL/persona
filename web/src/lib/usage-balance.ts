// What the sidebar's usage panel shows for a prepaid balance (#11825, docs/server/usage-windows.md "Prepaid balances"):
// the provider's amount and currency, never a percentage. Loaded with the popover, not with the first screen; the strip
// keeps only the few checks it needs inline.
import { isBalanceAccount, type UsageAccount, type UsageBalance } from "./usage-windows";

/** The provider's amount and currency as one string: "CNY 12.34". */
export function balanceText(b: UsageBalance): string {
  return `${b.currency} ${b.amount}`.trim();
}

/** A genuine zero balance: the account is out of prepaid credit, not unread. */
export function balanceEmpty(b: UsageBalance): boolean {
  return Number(b.amount) === 0;
}

/** The prepaid-balance accounts, in the hub's order, each shown as name + amount/currency. */
export function balanceAccounts(accounts: UsageAccount[]): UsageAccount[] {
  return accounts.filter(isBalanceAccount);
}
