package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// writeDeployed writes a deployed-commits file of repo o/r whose first commit is head, then the given lines.
func writeDeployed(t *testing.T, s *Service, head string, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), deployedCommitsName)
	text := deployedCommitsHeader + "\n# repo: o/r\n" + head + " fix: the deployed one (#20)\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	s.deploy.listPaths = []string{p}
	return p
}

func TestParseDeployedCommits(t *testing.T) {
	l := parseDeployedCommits(deployedCommitsHeader + "\n# repo: Owner/Repo.git\n" +
		newHub + " feat: the newest (#12)\n" +
		mergeSHA + " Merge pull request #9 from someone/branch\n" +
		oldHub + " fix: an older one (#12)\n" +
		"not a line\n")
	if l == nil || l.repo != "owner/repo" || l.head != newHub || l.count != 3 {
		t.Fatalf("%+v", l)
	}
	if l.prs[12] != newHub || l.prs[9] != mergeSHA || len(l.prs) != 2 {
		t.Fatalf("prs %+v", l.prs)
	}
	if !l.runs(newHub[:9]) || !l.runs(strings.ToUpper(newHub)) || l.runs(oldHub) || l.runs("cccc") {
		t.Fatal("runs")
	}
	for _, bad := range []string{"", "# repo: o/r\n" + newHub + " x (#1)\n", deployedCommitsHeader + "\n" + newHub + " x (#1)\n",
		deployedCommitsHeader + "\n# repo: o/r\n"} {
		if parseDeployedCommits(bad) != nil {
			t.Errorf("read %q as a file", bad)
		}
	}
}

// GitHub unreachable (the hub's machine has no way out): the deployed-commits file decides. The PR named in the running
// hub's file: deployed. Not named: held, and GitHub is not asked again for a while; once a deploy
// brings a file that names it, deployed.
func TestDeployedCommitsWithoutGitHub(t *testing.T) {
	s, f, posts, _, forge := deployService(t)
	forge.err = errors.New("dial tcp: connect: network is unreachable")
	writeDeployed(t, s, oldHub, mergeSHA+" fix: the task's PR (#9)")
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, deployedReply, 0)
	waitFor(t, "accepted", func() bool {
		a := askOf(s, todo.ID)
		return a != nil && !a.AcceptedAt.IsZero() && todoNow(s, todo.ID).Stage == "verify"
	})
	if forge.calls != 1 || verifyCards(posts()) != 0 {
		t.Fatalf("%d GitHub calls, %d cards", forge.calls, verifyCards(posts()))
	}

	s2, f2, _, hub2, forge2 := deployService(t)
	forge2.err = errors.New("dial tcp: connect: network is unreachable")
	writeDeployed(t, s2, oldHub, "dddddddddddddddddddddddddddddddddddddddd fix: something else (#8)")
	todo2 := startedTask(t, s2, f2)
	agentTurn(s2, f2, proto.StatusWorking, "", 0)
	agentTurn(s2, f2, proto.StatusDone, deployedReply, 0)
	waitFor(t, "held", func() bool { return todoNow(s2, todo2.ID).Delivery != nil })
	got := todoNow(s2, todo2.ID)
	if got.Delivery.State != deliveryPR || got.Stage != "review" || !strings.Contains(got.Delivery.Gate, "还没有这个 PR") {
		t.Fatalf("%q %+v", got.Stage, got.Delivery)
	}
	if askOf(s2, todo2.ID) != nil {
		t.Fatal("asked to verify")
	}
	calls := forge2.calls
	s2.sweepDeploys(time.Now())
	if forge2.calls != calls {
		t.Fatalf("GitHub asked again %d times while it is down", forge2.calls-calls)
	}
	hub2.deploy(newHub)
	writeDeployed(t, s2, newHub, mergeSHA+" fix: the task's PR (#9)", oldHub+" the one before")
	s2.sweepDeploys(time.Now())
	if got := todoNow(s2, todo2.ID); got.Delivery.State != deliveryLive || got.Stage != "verify" {
		t.Fatalf("after the deploy: %q %+v", got.Stage, got.Delivery)
	}
}

// A file of another deploy (its first commit is not the running hub's) is not believed: GitHub decides, and when it
// cannot, nothing says deployed.
func TestStaleDeployedCommitsIgnored(t *testing.T) {
	s, f, _, _, forge := deployService(t)
	forge.err = errors.New("timeout")
	writeDeployed(t, s, newHub, mergeSHA+" fix: the task's PR (#9)") // the hub runs oldHub
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, deployedReply, 0)
	waitFor(t, "held", func() bool { return todoNow(s, todo.ID).Delivery != nil })
	if got := todoNow(s, todo.ID); got.Delivery.State != deliveryPR || !strings.Contains(got.Delivery.Gate, "等确认部署") {
		t.Fatalf("%+v", got.Delivery)
	}
}

// The open to-dos' sweep with GitHub unreachable: a PR the file names is deployed (Just finished); one it does not name
// is left as it is (it may be open still), never "merged, not deployed".
func TestSweepPRsWithDeployedCommits(t *testing.T) {
	s, _, _, _, forge := deployService(t)
	forge.err = errors.New("unreachable")
	writeDeployed(t, s, oldHub, mergeSHA+" fix: progress for every task (#9)")
	ctx := t.Context()
	putTodo(s, Todo{ID: "todo_in", Title: "主对话任务栏显示全部进度", Origin: "conv1", CreatedAt: time.Now().Add(-time.Hour)})
	s.setTaskStage(ctx, "todo_in", "review", "https://github.com/o/r/pull/9")
	putTodo(s, Todo{ID: "todo_open", Title: "开着的", Origin: "conv1", CreatedAt: time.Now().Add(-time.Hour)})
	s.setTaskStage(ctx, "todo_open", "review", "https://github.com/o/r/pull/31")
	s.sweepDeploys(time.Now())
	if got := todoNow(s, "todo_in"); got.Delivery == nil || got.Delivery.State != deliveryLive || got.Stage != "deploy" {
		t.Fatalf("named: %q %+v", got.Stage, got.Delivery)
	}
	if got := todoNow(s, "todo_open"); got.Delivery != nil || got.Stage != "review" {
		t.Fatalf("not named: %q %+v", got.Stage, got.Delivery)
	}
}

// A local build runs a PR before it is merged upstream (#4867): the deploy names it by its head commit, "# pr: <N> <sha>",
// and that counts only when the file lists the commit (the deployed one's history). A merge the file names by its
// subject wins over a head line.
func TestDeployedCommitsPRHeads(t *testing.T) {
	head, unlisted := strings.Repeat("d", 40), strings.Repeat("e", 40)
	l := parseDeployedCommits(deployedCommitsHeader + "\n# repo: o/r\n# pr: 31 " + strings.ToUpper(head) + "\n# pr: #32 " + unlisted +
		"\n# pr: 9 " + oldHub + "\n# pr: x " + head + "\n" +
		newHub + " Merge branch 'fix/31-thing' into local\n" +
		head + " feat: the open PR's head\n" +
		mergeSHA + " Merge pull request #9 from someone/branch\n" +
		oldHub + " fix: an older one (#12)\n")
	if l == nil || l.head != newHub || l.count != 4 {
		t.Fatalf("%+v", l)
	}
	if l.prs[31] != head || l.prs[9] != mergeSHA || l.prs[12] != oldHub || len(l.prs) != 3 {
		t.Fatalf("prs %+v", l.prs)
	}
}

// GitHub would say the PR is still open; the running hub's file names it by its head: deployed here, asked to verify
// and verified by the supervising assistant. A file that does not list the head is no evidence:
// held at the PR.
func TestDeployedBeforeTheUpstreamMerge(t *testing.T) {
	s, f, _, _, forge := deployService(t)
	forge.merged, forge.sha = false, ""
	head := strings.Repeat("d", 40)
	writeDeployed(t, s, oldHub, head+" feat: the task's PR, merged into the local branch", "# pr: 9 "+head)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, deployedReply, 0)
	waitFor(t, "verified", func() bool {
		a := askOf(s, todo.ID)
		return a != nil && !a.AcceptedAt.IsZero() && todoNow(s, todo.ID).Stage == "verify"
	})
	if got := todoNow(s, todo.ID); got.Delivery != nil && got.Delivery.State != deliveryLive || forge.calls != 1 {
		t.Fatalf("%+v, %d GitHub calls", got.Delivery, forge.calls)
	}

	s2, f2, _, _, forge2 := deployService(t)
	forge2.merged, forge2.sha = false, ""
	writeDeployed(t, s2, oldHub, "# pr: 9 "+head) // its head is not in the deployed history
	todo2 := startedTask(t, s2, f2)
	agentTurn(s2, f2, proto.StatusWorking, "", 0)
	agentTurn(s2, f2, proto.StatusDone, deployedReply, 0)
	waitFor(t, "held", func() bool { return todoNow(s2, todo2.ID).Delivery != nil })
	if got := todoNow(s2, todo2.ID); got.Delivery.State != deliveryPR || got.Stage != "review" || askOf(s2, todo2.ID) != nil {
		t.Fatalf("%q %+v", got.Stage, got.Delivery)
	}
}

func TestCloseoutReportsLocalDeploymentAndUpstreamMergeIndependently(t *testing.T) {
	for _, tc := range []struct {
		name, merged, text string
		forgeErr           error
	}{
		{"open", proto.CloseoutNo, "没合并，本机部署包含该 PR", nil},
		{"unavailable", "", "合没合查不出，本机部署包含该 PR", errors.New("timeout")},
		{"merged", proto.CloseoutYes, "已合并，本机部署包含该 PR", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _, forge := deployService(t)
			forge.merged, forge.err = tc.merged == proto.CloseoutYes, tc.forgeErr
			head := strings.Repeat("d", 40)
			writeDeployed(t, s, oldHub, head+" local change", "# pr: 9 "+head)
			var c proto.Closeout
			s.checkCloseout(context.Background(), &c, []proto.WorkArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/9"}})
			if c.Checked == nil || c.Checked.Merged != tc.merged || c.Checked.Deployed != proto.CloseoutYes {
				t.Fatalf("merge and deploy facts: %+v", c.Checked)
			}
			line := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{{Closeout: &prompts.SuperviseCloseout{
				CheckedMerged: c.Checked.Merged, CheckedDeployed: c.Checked.Deployed, LocalDeployment: c.Checked.LocalDeployment,
			}}}})
			if !strings.Contains(line, tc.text) {
				t.Errorf("want %q in supervision message: %s", tc.text, line)
			}
		})
	}
}
