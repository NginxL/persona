package kb

import (
	"errors"
	"slices"
	"testing"
)

// excludeEngine has project billing-migration with alice and bob, team-chat and ops-chat, linked both ways.
func excludeEngine(t *testing.T) *Engine {
	t.Helper()
	e := editEngine(t)
	for _, id := range []string{"alice", "bob", "carol"} {
		p := &Person{ID: id, Name: id}
		if id != "carol" {
			p.Projects = []string{"billing-migration"}
		}
		if err := e.Store.PutPerson(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"team-chat", "ops-chat", "new-chat"} {
		c := &Chat{ID: id, Name: id, Kind: "group"}
		if id != "new-chat" {
			c.Projects = []string{"billing-migration"}
		}
		if err := e.Store.PutChat(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Store.PutProject(&Project{Slug: "billing-migration", Name: "billing-migration", Status: "active",
		People: []string{"alice", "bob"}, Chats: []string{"team-chat", "ops-chat"}}); err != nil {
		t.Fatal(err)
	}
	return e
}

func applyUser(t *testing.T, e *Engine, kind, id string, u UserUpdate) {
	t.Helper()
	if _, err := e.ApplyUser(kind, id, u); err != nil {
		t.Fatal(err)
	}
}

func project(t *testing.T, e *Engine) *Project {
	t.Helper()
	p, ok := e.Store.Project("billing-migration")
	if !ok {
		t.Fatal("billing-migration is gone")
	}
	return p
}

// A removal with lock_lists false keeps out just that link, on both entries: automatic writes still add and remove
// everything else in the lists, and a stale pipeline object neither brings the link back nor drops the record.
func TestRemovalExcludesOnlyThatLink(t *testing.T) {
	e := excludeEngine(t)
	applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{RemovePeople: []string{"alice"}, RemoveChats: []string{"team-chat"}}, LockLists: new(false)})

	p := project(t, e)
	if !slices.Equal(p.People, []string{"bob"}) || !slices.Equal(p.Chats, []string{"ops-chat"}) {
		t.Fatalf("links after the removal: people %v, chats %v", p.People, p.Chats)
	}
	if len(p.LockedFields) != 0 {
		t.Fatalf("a per-link removal locked %v", p.LockedFields)
	}
	if !slices.Equal(p.Excluded["people"], []string{"alice"}) || !slices.Equal(p.Excluded["chats"], []string{"team-chat"}) {
		t.Fatalf("project excluded %v", p.Excluded)
	}
	alice, _ := e.Store.Person("alice")
	chat, _ := e.Store.Chat("team-chat")
	if len(alice.Projects)+len(chat.Projects) != 0 || len(alice.LockedFields)+len(chat.LockedFields) != 0 {
		t.Fatalf("other side: alice %v %v, team-chat %v %v", alice.Projects, alice.LockedFields, chat.Projects, chat.LockedFields)
	}
	if !alice.excludes("projects", "billing-migration") || !chat.excludes("projects", "billing-migration") {
		t.Fatalf("other side not excluded: alice %v, team-chat %v", alice.Excluded, chat.Excluded)
	}

	// The pipeline links the whole team again plus newcomers, from an object read before the removal.
	stale := &Project{Slug: "billing-migration", Name: "billing-migration", Status: "active",
		People: []string{"alice", "bob", "carol"}, Chats: []string{"team-chat", "ops-chat", "new-chat"}}
	if err := e.Store.PutProject(stale); err != nil {
		t.Fatal(err)
	}
	p = project(t, e)
	if !slices.Equal(p.People, []string{"bob", "carol"}) || !slices.Equal(p.Chats, []string{"ops-chat", "new-chat"}) {
		t.Fatalf("automatic write: people %v, chats %v", p.People, p.Chats)
	}
	if !p.excludes("people", "alice") || !p.excludes("chats", "team-chat") {
		t.Fatalf("a stale write dropped the exclusions: %v", p.Excluded)
	}
	// Automatic removals of what the user did not touch go on too.
	p.People = []string{"carol"}
	if err := e.Store.PutProject(p); err != nil {
		t.Fatal(err)
	}
	if p = project(t, e); !slices.Equal(p.People, []string{"carol"}) {
		t.Fatalf("automatic removal: %v", p.People)
	}

	alice.Projects = []string{"billing-migration", "other"}
	chat.Projects = []string{"billing-migration"}
	if err := e.Store.PutPerson(alice); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.PutChat(chat); err != nil {
		t.Fatal(err)
	}
	alice, _ = e.Store.Person("alice")
	chat, _ = e.Store.Chat("team-chat")
	if !slices.Equal(alice.Projects, []string{"other"}) || len(chat.Projects) != 0 {
		t.Fatalf("automatic writes on the other side: alice %v, team-chat %v", alice.Projects, chat.Projects)
	}
}

// A removal that does not say lock_lists locks as lock says, as it did before exclusions; lock_lists true locks too.
// Either way the link is excluded as well.
func TestRemovalLocksListsUnlessToldNot(t *testing.T) {
	for name, u := range map[string]UserUpdate{
		"unset":           {Update: Update{RemovePeople: []string{"alice"}}},
		"lock_lists true": {Update: Update{RemovePeople: []string{"alice"}, Lock: new(false)}, LockLists: new(true)},
	} {
		t.Run(name, func(t *testing.T) {
			e := excludeEngine(t)
			applyUser(t, e, KindProject, "billing-migration", u)
			p := project(t, e)
			alice, _ := e.Store.Person("alice")
			if !slices.Contains(p.LockedFields, "people") || !slices.Contains(alice.LockedFields, "projects") {
				t.Fatalf("lists not locked: project %v, alice %v", p.LockedFields, alice.LockedFields)
			}
			if !p.excludes("people", "alice") || !alice.excludes("projects", "billing-migration") {
				t.Fatalf("not excluded: project %v, alice %v", p.Excluded, alice.Excluded)
			}
			p.People = []string{"bob", "carol"}
			_ = e.Store.PutProject(p)
			if p = project(t, e); !slices.Equal(p.People, []string{"bob"}) {
				t.Fatalf("a locked list changed: %v", p.People)
			}
		})
	}
	// Apply (other user paths, which have no lock_lists) keeps the old meaning too.
	e := excludeEngine(t)
	editApply(t, e, KindProject, "billing-migration", Update{RemoveChats: []string{"team-chat"}})
	if p := project(t, e); !slices.Contains(p.LockedFields, "chats") {
		t.Fatalf("Apply did not lock: %v", p.LockedFields)
	}
}

// Linking by hand lifts the exclusion on both entries; allowing again lifts it without linking.
func TestLinkingOrAllowingLiftsExclusion(t *testing.T) {
	e := excludeEngine(t)
	off := new(false)
	applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{RemovePeople: []string{"alice", "bob"}, RemoveChats: []string{"team-chat"}}, LockLists: off})

	applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{People: []string{"alice"}, Lock: off}})
	p := project(t, e)
	alice, _ := e.Store.Person("alice")
	if !slices.Contains(p.People, "alice") || p.excludes("people", "alice") || alice.excludes("projects", "billing-migration") {
		t.Fatalf("linking again: people %v, project excluded %v, alice excluded %v", p.People, p.Excluded, alice.Excluded)
	}

	applyUser(t, e, KindProject, "billing-migration", UserUpdate{AllowPeople: []string{"bob"}, AllowChats: []string{"team-chat"}})
	p = project(t, e)
	bob, _ := e.Store.Person("bob")
	chat, _ := e.Store.Chat("team-chat")
	if slices.Contains(p.People, "bob") || slices.Contains(p.Chats, "team-chat") {
		t.Fatalf("allowing linked: people %v, chats %v", p.People, p.Chats)
	}
	if len(p.Excluded) != 0 || len(bob.Excluded) != 0 || len(chat.Excluded) != 0 {
		t.Fatalf("allowing left exclusions: project %v, bob %v, team-chat %v", p.Excluded, bob.Excluded, chat.Excluded)
	}
	p.People = append(p.People, "bob")
	_ = e.Store.PutProject(p)
	if p = project(t, e); !slices.Contains(p.People, "bob") {
		t.Fatal("the pipeline cannot link an allowed person")
	}

	// From the chat's side: allow_projects lifts both entries too.
	applyUser(t, e, KindChat, "ops-chat", UserUpdate{Update: Update{RemoveProjects: []string{"billing-migration"}}, LockLists: off})
	applyUser(t, e, KindChat, "ops-chat", UserUpdate{AllowProjects: []string{"billing-migration"}})
	c, _ := e.Store.Chat("ops-chat")
	if p = project(t, e); len(c.Excluded) != 0 || p.excludes("chats", "ops-chat") {
		t.Fatalf("allow_projects: chat %v, project %v", c.Excluded, p.Excluded)
	}
}

// Linking a chat by hand brings its people, but not the ones the user took off the project.
func TestLinkingAChatKeepsRemovedPeopleOut(t *testing.T) {
	e := excludeEngine(t)
	c, _ := e.Store.Chat("new-chat")
	c.People = []string{"alice", "carol"}
	_ = e.Store.PutChat(c)
	applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{RemovePeople: []string{"alice"}}, LockLists: new(false)})
	applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{Chats: []string{"new-chat"}}})
	if p := project(t, e); slices.Contains(p.People, "alice") || !slices.Contains(p.People, "carol") {
		t.Fatalf("people after linking new-chat: %v", p.People)
	}
}

// Chats and people, and items and projects, are kept apart the same way.
func TestRemovalExcludesChatPeopleAndItems(t *testing.T) {
	e := excludeEngine(t)
	c, _ := e.Store.Chat("team-chat")
	c.People = []string{"alice", "bob"}
	_ = e.Store.PutChat(c)
	_ = e.Store.PutItem(&Item{Kind: KindDoc, ID: "design-doc", Title: "Design", Projects: []string{"billing-migration"}})
	p := project(t, e)
	p.Items = []string{"docs/design-doc"}
	_ = e.Store.PutProject(p)

	off := new(false)
	applyUser(t, e, KindChat, "team-chat", UserUpdate{Update: Update{RemovePeople: []string{"alice"}}, LockLists: off})
	applyUser(t, e, KindDoc, "design-doc", UserUpdate{Update: Update{RemoveProjects: []string{"billing-migration"}}, LockLists: off})

	c, _ = e.Store.Chat("team-chat")
	c.People = append(c.People, "alice", "carol")
	_ = e.Store.PutChat(c)
	if c, _ = e.Store.Chat("team-chat"); !slices.Equal(c.People, []string{"bob", "carol"}) {
		t.Fatalf("chat people: %v", c.People)
	}
	alice, _ := e.Store.Person("alice")
	if !alice.excludes("chats", "team-chat") {
		t.Fatalf("alice excluded %v", alice.Excluded)
	}
	it, _ := e.Store.Item(KindDoc, "design-doc")
	it.Projects = []string{"billing-migration"}
	_ = e.Store.PutItem(it)
	p = project(t, e)
	p.Items = []string{"docs/design-doc"}
	_ = e.Store.PutProject(p)
	it, _ = e.Store.Item(KindDoc, "design-doc")
	if p = project(t, e); len(it.Projects)+len(p.Items) != 0 || !p.excludes("items", "docs/design-doc") {
		t.Fatalf("item links: doc %v, project items %v, excluded %v", it.Projects, p.Items, p.Excluded)
	}
}

// An extension cannot make a link the user removed: the rest of its update applies and it is told why. Its own
// removals exclude nothing.
func TestExtensionCannotRelinkWhatTheUserRemoved(t *testing.T) {
	e := excludeEngine(t)
	applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{RemovePeople: []string{"alice"}}, LockLists: new(false)})
	res, err := e.ApplyExtension(KindProject, "billing-migration", Update{People: []string{"alice", "carol"}, Lock: new(false)}, ExtensionWrite{Extension: "curator"})
	if err != nil {
		t.Fatal(err)
	}
	if p := project(t, e); slices.Contains(p.People, "alice") || !slices.Contains(p.People, "carol") {
		t.Fatalf("people after the extension: %v", p.People)
	}
	if len(res.Ignored) != 1 || res.Ignored[0].Field != "people" {
		t.Fatalf("ignored: %+v", res.Ignored)
	}
	// From the person's side the same.
	res, err = e.ApplyExtension(KindPerson, "alice", Update{Projects: []string{"billing-migration"}, Lock: new(false)}, ExtensionWrite{Extension: "curator"})
	if err != nil {
		t.Fatal(err)
	}
	if alice, _ := e.Store.Person("alice"); slices.Contains(alice.Projects, "billing-migration") || len(res.Ignored) != 1 {
		t.Fatalf("alice after the extension: %v, ignored %+v", alice.Projects, res.Ignored)
	}
	if _, err := e.ApplyExtension(KindProject, "billing-migration", Update{RemovePeople: []string{"bob"}, Lock: new(false)}, ExtensionWrite{Extension: "curator"}); err != nil {
		t.Fatal(err)
	}
	if p := project(t, e); p.excludes("people", "bob") {
		t.Fatalf("an extension's removal excluded: %v", p.Excluded)
	}
}

// The detail names the removed links, for the app to list and allow again.
func TestDetailNamesRemovedLinks(t *testing.T) {
	e := excludeEngine(t)
	applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{RemovePeople: []string{"alice"}, RemoveChats: []string{"team-chat"}}, LockLists: new(false)})
	d, err := e.Get(KindProject, "billing-migration", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range d.Removed {
		got = append(got, x.Kind+"/"+x.ID+"="+x.Name)
	}
	if !slices.Equal(got, []string{"chats/team-chat=team-chat", "people/alice=alice"}) {
		t.Fatalf("removed: %v", got)
	}
	for _, x := range d.Links {
		if x.ID == "alice" || x.ID == "team-chat" {
			t.Fatalf("a removed link is still listed: %+v", x)
		}
	}
	if d, _ = e.Get(KindPerson, "alice", 0); len(d.Removed) != 1 || d.Removed[0].ID != "billing-migration" {
		t.Fatalf("alice's removed: %+v", d.Removed)
	}
}

// A removal of something that is no entry (a name instead of an id) records nothing; allow_* a kind has no list for
// is said and not applied.
func TestRemovalRecordsOnlyEntriesAndAllowFieldsByKind(t *testing.T) {
	e := excludeEngine(t)
	applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{RemovePeople: []string{"Alice Example"}}, LockLists: new(false)})
	if p := project(t, e); len(p.Excluded) != 0 {
		t.Fatalf("excluded a name: %v", p.Excluded)
	}
	ignored := UserUnsupported(KindProject, UserUpdate{AllowProjects: []string{"x"}, AllowPeople: []string{"alice"}})
	if len(ignored) != 1 || ignored[0].Field != "allow_projects" {
		t.Fatalf("ignored: %+v", ignored)
	}
	if ignored := UserUnsupported(KindTopic, UserUpdate{AllowChats: []string{"x"}}); len(ignored) != 1 {
		t.Fatalf("topic ignored: %+v", ignored)
	}
}

// A merge, automatic or by hand, keeps what the user removed from either project off the kept one, both ways, and an
// unmerge lets back what came with the merge.
func TestMergesKeepRemovedLinksOut(t *testing.T) {
	for _, how := range []string{"automatic", "by hand"} {
		t.Run(how, func(t *testing.T) {
			e := excludeEngine(t)
			off := new(false)
			_ = e.Store.PutPerson(&Person{ID: "dave", Name: "dave", Projects: []string{"billing-cutover"}})
			if err := e.Store.PutProject(&Project{Slug: "billing-cutover", Name: "billing-cutover", Status: "active", People: []string{"alice", "dave"}}); err != nil {
				t.Fatal(err)
			}
			a, _ := e.Store.Person("alice")
			a.Projects = append(a.Projects, "billing-cutover")
			_ = e.Store.PutPerson(a)
			applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{RemovePeople: []string{"alice"}}, LockLists: off})
			applyUser(t, e, KindProject, "billing-cutover", UserUpdate{Update: Update{RemovePeople: []string{"dave"}}, LockLists: off})

			if how == "automatic" {
				if err := e.MergeProjects("billing-migration", "billing-cutover", "same work"); err != nil {
					t.Fatal(err)
				}
			} else {
				applyUser(t, e, KindProject, "billing-cutover", UserUpdate{Update: Update{MergeInto: "billing-migration"}})
			}
			p := project(t, e)
			alice, _ := e.Store.Person("alice")
			dave, _ := e.Store.Person("dave")
			if slices.Contains(p.People, "alice") || slices.Contains(alice.Projects, "billing-migration") {
				t.Fatalf("the merge brought alice back: project %v, alice %v", p.People, alice.Projects)
			}
			if !p.excludes("people", "dave") || !dave.excludes("projects", "billing-migration") {
				t.Fatalf("dave's removal did not carry: project %v, dave %v", p.Excluded, dave.Excluded)
			}
			p.People = append(p.People, "dave", "alice")
			_ = e.Store.PutProject(p)
			if p = project(t, e); slices.Contains(p.People, "dave") || slices.Contains(p.People, "alice") {
				t.Fatalf("automatic write after the merge: %v", p.People)
			}

			if _, err := e.Unmerge("billing-cutover"); err != nil {
				t.Fatal(err)
			}
			p = project(t, e)
			dave, _ = e.Store.Person("dave")
			if p.excludes("people", "dave") || dave.excludes("projects", "billing-migration") || !dave.excludes("projects", "billing-cutover") {
				t.Fatalf("after unmerge: project %v, dave %v", p.Excluded, dave.Excluded)
			}
			if !p.excludes("people", "alice") {
				t.Fatalf("unmerge lifted the kept project's own removal: %v", p.Excluded)
			}
		})
	}
}

// Forgetting a source takes its ids out of the removed links too.
func TestForgetDropsRemovedLinksOfTheSource(t *testing.T) {
	e := editEngine(t)
	_ = e.Store.PutPerson(&Person{ID: "lark-alice", Name: "alice", Source: "lark", Projects: []string{"billing-migration"}})
	_ = e.Store.PutPerson(&Person{ID: "mail-bob", Name: "bob", Source: "mail"})
	_ = e.Store.PutChat(&Chat{ID: "mail-team", Name: "team-chat", Source: "mail", People: []string{"lark-alice"}})
	_ = e.Store.PutProject(&Project{Slug: "billing-migration", Name: "billing-migration", Status: "active", People: []string{"lark-alice", "mail-bob"}})
	off := new(false)
	applyUser(t, e, KindProject, "billing-migration", UserUpdate{Update: Update{RemovePeople: []string{"lark-alice"}}, LockLists: off})
	applyUser(t, e, KindChat, "mail-team", UserUpdate{Update: Update{RemovePeople: []string{"lark-alice"}}, LockLists: off})
	if _, err := e.ForgetSource("lark", "lark"); err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	c, _ := e.Store.Chat("mail-team")
	if p := project(t, e); len(p.Excluded)+len(c.Excluded) != 0 {
		t.Fatalf("forgotten ids stay excluded: project %v, chat %v", p.Excluded, c.Excluded)
	}
}
