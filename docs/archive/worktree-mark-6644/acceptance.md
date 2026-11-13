# Worktree sidebar mark: evidence for #6644

Screenshots showing the By-repository sidebar after removing the bare git-branch glyph
from non-main worktree rows. The main checkout keeps its home marker and Primary badge;
other worktrees are text-only with tree indentation.

- `web-worktree-rows-{light,dark}.png` — web app against the mock hub (`scripts/mock/mock_hub.py`,
  `MOCK_SESSIONS_LIST=1 MOCK_SESSIONS_CATALOG=1`), Playwright/Chromium, English locale.
  The same repository shows the main checkout row with the home marker and a non-main
  worktree (`Weather\core`) with no leading glyph.
- `repo-tree-{glass,brief,board}-{light,dark}.png` — Android Robolectric screenshot tests
  (`RepoTreeLinesShotsTest`, SDK 35, JDK 21), every app style in light and dark.
  `main` keeps the house mark and Primary tag; `bisect` (Detached HEAD), `fix/sync`,
  `Speed up the importer` and the offline `hotfix` rows are text-only.

iOS simulator screenshots could not be produced on the available Linux machines (no Xcode);
the iOS change is view-only and covered by `swiftc -parse` plus the same alignment logic.
