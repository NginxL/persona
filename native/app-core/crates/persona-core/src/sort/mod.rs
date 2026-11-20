//! The Agents list's filters, order, groups and folds (`spec/fixtures/sort/`).
//!
//! This is a projection of values supplied by a client: it does not read preferences, a clock or a machine registry.
//! The rules follow `AgentListOrder`, `AgentListProjection`, `SpaceGroup` and `ChatsView` on iOS.

use std::cmp::Ordering;
use std::collections::{BTreeMap, BTreeSet, HashMap as StdHashMap, HashSet};

use serde::{Deserialize, Serialize};

mod session;

pub use session::Session;

use crate::state::{AgentState, utc_timestamp};

/// The sessions, registry and preferences from which one Agents list is drawn.
#[derive(Clone, Debug, Deserialize)]
pub struct Input {
    pub sessions: Vec<Session>,
    pub machines: Vec<Machine>,
    pub pins: Pins,
    pub mutes: Mutes,
    /// iOS's per-hub preferences. The legacy single-hub maps above remain the default hub's input; each entry here
    /// replaces both for its group, so a pin on one hub cannot pin the same machine/session on another.
    #[serde(default)]
    pub hub_preferences: Option<StdHashMap<String, HubPreferences>>,
    /// Manager-scoped preferences, applied only to the matching group + manager. Hub preferences remain the fallback.
    #[serde(default)]
    pub manager_preferences: Option<StdHashMap<String, HubPreferences>>,
    pub settings: Settings,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Machine {
    pub id: String,
    pub name: String,
    /// The hub group this machine belongs to, if not the default. `group|id` forms the full path,
    /// matching iOS's `MachineRef.path`.
    #[serde(default)]
    pub group: Option<String>,
    /// An owner-qualified machine registry entry. Legacy input without it retains `group|id` exactly.
    #[serde(default)]
    pub manager: Option<String>,
}

/// The owner-qualified path: `group|manager|id` when manager is supplied, otherwise the existing `group|id`.
pub fn machine_path(group: Option<&str>, manager: Option<&str>, id: &str) -> String {
    match manager {
        Some(manager) if !manager.is_empty() => match group {
            Some(group) if !group.is_empty() => format!("{group}|{manager}|{id}"),
            _ => format!("{manager}|{id}"),
        },
        _ => match group {
            Some(group) if !group.is_empty() => format!("{group}|{id}"),
            _ => id.to_owned(),
        },
    }
}

impl Machine {
    fn path(&self) -> String {
        machine_path(self.group.as_deref(), self.manager.as_deref(), &self.id)
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Pins {
    #[serde(default)]
    pub spaces: Vec<String>,
    #[serde(default)]
    pub sessions: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct HubPreferences {
    #[serde(default)]
    pub pins: Pins,
    #[serde(default)]
    pub mutes: Mutes,
}

impl Input {
    fn preferences(&self, group: &str, manager: Option<&str>) -> (&Pins, &Mutes) {
        manager
            .filter(|manager| !manager.is_empty())
            .and_then(|manager| {
                let key = format!("{group}|{manager}");
                self.manager_preferences.as_ref()?.get(&key)
            })
            .map_or_else(
                || {
                    self.hub_preferences
                        .as_ref()
                        .and_then(|preferences| preferences.get(group))
                        .map_or((&self.pins, &self.mutes), |preferences| {
                            (&preferences.pins, &preferences.mutes)
                        })
                },
                |preferences| (&preferences.pins, &preferences.mutes),
            )
    }

    fn pinned(&self, session: &Session) -> bool {
        let (pins, _) = self.preferences(
            session.app_hub_group.as_deref().unwrap_or(""),
            session.manager.as_deref(),
        );
        pins.sessions.iter().any(|pin| session.matches_preference(pin))
    }

    fn muted(&self, session: &Session) -> bool {
        let (_, mutes) = self.preferences(
            session.app_hub_group.as_deref().unwrap_or(""),
            session.manager.as_deref(),
        );
        if mutes.sessions.iter().any(|id| session.matches_preference(id)) {
            return true;
        }
        if mutes.allowed.iter().any(|id| session.matches_preference(id)) {
            return false;
        }
        let space = format!(
            "{}/{}",
            session.machine_id,
            session.space_name.clone().unwrap_or_else(|| folder_name(&session.cwd))
        );
        mutes.spaces.contains(&space)
    }

    fn pinned_space(&self, space: &Space<'_>) -> bool {
        let (pins, _) = self.preferences(&space.group, space.manager.as_deref());
        pins.spaces.contains(&format!("{}/{}", space.machine_id, space.name))
    }

    fn muted_space(&self, space: &Space<'_>) -> bool {
        let (_, mutes) = self.preferences(&space.group, space.manager.as_deref());
        mutes.spaces.contains(&format!("{}/{}", space.machine_id, space.name))
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Mutes {
    #[serde(default)]
    pub spaces: Vec<String>,
    #[serde(default)]
    pub sessions: Vec<String>,
    #[serde(default)]
    pub allowed: Vec<String>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Settings {
    pub grouping: Grouping,
    pub quick: Quick,
    /// `sort=updated` disables row rank, as on iOS; the default keeps the fixtures' state ordering.
    #[serde(default = "state_sort")]
    pub sort: String,
    #[serde(default)]
    pub query: String,
    #[serde(default)]
    pub search_scope: SearchScope,
    #[serde(default)]
    pub search_as_code: bool,
    #[serde(default)]
    pub machine_filter: String,
    /// The selected machine also participates in iOS's title-capable `machine:` predicate.
    #[serde(default)]
    pub machine_title_filter: String,
    #[serde(default)]
    pub harness_filter: String,
    #[serde(default)]
    pub project_filter: String,
    #[serde(default)]
    pub model_filter: String,
    #[serde(default)]
    pub pinned_only: bool,
    /// iOS's merged-list hub selection is active only in multi-hub mode. An empty filter selects every group;
    /// `=<group>` selects one group, with `=` selecting the default empty group.
    #[serde(default)]
    pub multi_hub: bool,
    #[serde(default)]
    pub hub_filter: String,
    /// Archived history may include offline rows, without making ordinary offline live sessions visible.
    #[serde(default)]
    pub history: bool,
    #[serde(default)]
    pub kept: Vec<String>,
    #[serde(default)]
    pub folded: Vec<String>,
    #[serde(default)]
    pub expanded_idle: Vec<String>,
    #[serde(default)]
    pub expanded_quiet: Vec<String>,
    /// The row style does not affect the data projection.
    #[serde(default)]
    pub compact: bool,
    /// Opt into the real By Project tab rows. Legacy fixtures omit it and keep flat session rows.
    #[serde(default)]
    pub presentation_rows: bool,
    /// Include filtered/unpinned inventory even when no renderable group can contain a row.
    #[serde(default)]
    pub inventory: bool,
    /// Paged spaces whose hub totals may not have arrived yet. Missing totals are zero, not local-window counts.
    #[serde(default)]
    pub bounded_spaces: BTreeSet<String>,
    /// Counts supplied by a paged list, keyed by the same space id as its sessions.
    #[serde(default)]
    pub aggregate: StdHashMap<String, AggregateCounts>,
    /// The open agent that was Unread when it was opened (`AppModel.heldInList`): it keeps its place and stays
    /// out of the idle folds until its chat is left (#2213).
    #[serde(default)]
    pub held: Option<String>,
    /// The caller's current time as an ISO 8601 string, used only by the `since:` / `after:` query token.
    /// The projection itself never reads a clock; without this field `since:` matches nothing.
    #[serde(default)]
    pub now: Option<String>,
}

#[derive(Clone, Copy, Debug, Default, Eq, PartialEq, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SearchScope {
    #[default]
    All,
    Title,
    Conversation,
    User,
    Assistant,
    Errors,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Grouping {
    State,
    Space,
    Machine,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Deserialize)]
pub enum Quick {
    #[serde(rename = "all")]
    All,
    #[serde(rename = "needsYou")]
    NeedsYou,
    #[serde(rename = "unread")]
    Unread,
    #[serde(rename = "working")]
    Working,
    #[serde(rename = "backgroundWaiting")]
    BackgroundWaiting,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct Projection {
    pub counts: Counts,
    pub groups: Vec<Group>,
    /// Real tab and agent rows of the compact By Project list; omitted for the legacy flat contract.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub rows: Option<Vec<Entry>>,
    pub unread_shown: Vec<String>,
    /// Quick-filtered roots, independent of whether the selected grouping can render them.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub filtered: Option<Vec<String>>,
    /// The same inventory without pinned roots; present with `filtered`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unpinned: Option<Vec<String>>,
    /// By Project always carries this field; other groupings omit it.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub quiet_title: Option<Option<String>>,
}

fn state_sort() -> String {
    "state".to_owned()
}

#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize)]
pub struct Counts {
    #[serde(rename = "needsYou")]
    pub needs_you: usize,
    pub unread: usize,
    pub working: usize,
    #[serde(rename = "backgroundWaiting")]
    pub background_waiting: usize,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct Entry {
    pub id: String,
    pub fold_id: String,
    pub rows: Vec<String>,
    pub visible_rows: Vec<String>,
    pub folded: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct AggregateCounts {
    #[serde(default)]
    pub state: StdHashMap<String, usize>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct Group {
    pub id: String,
    pub title: String,
    pub rows: Vec<String>,
    pub visible_rows: Vec<String>,
    pub folded: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub parent: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub state_counts: Option<StateCounts>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub spoken: Option<String>,
}

#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize)]
pub struct StateCounts {
    pub needs_you: usize,
    pub working: usize,
    pub unread: usize,
    pub idle: usize,
    pub unknown: usize,
}

#[derive(Clone, Debug)]
struct Row {
    id: String,
    short_id: String,
    machine_id: String,
    session_id: String,
    /// `group|machine_id` for a non-default hub group, as iOS's `MachineRef.path`.
    machine_path: String,
    group: String,
    manager: Option<String>,
    state: AgentState,
    /// Archive history's membership flag; ordinary offline rows remain excluded.
    listed: bool,
    archived: bool,
    sort_date: Option<i128>,
    is_waiting: bool,
    background_waiting: bool,
    nested_subtask: bool,
    space_kind: String,
    pane_id: Option<String>,
    pane_index: Option<i64>,
    pinned: bool,
    /// The open agent that was Unread when opened (`AppModel.heldInList`): keeps its rank-1 position,
    /// stays visible, and prevents its space from folding into quiet spaces (#2213).
    held: bool,
    muted: bool,
    awaits_reply: bool,
    just_finished: bool,
    space_id: String,
    space_name: String,
    slot_index: Option<i64>,
    slot_id: String,
}

impl Row {
    fn new(session: Session, input: &Input) -> Self {
        let state = AgentState::from_client(session.state.as_deref(), session.status.as_deref());
        let short_id = format!("{}/{}", session.machine_id, session.session_id);
        let pinned = input.pinned(&session);
        let muted = input.muted(&session);
        let space_name = session.space_name.clone().unwrap_or_else(|| folder_name(&session.cwd));
        let group = session.app_hub_group.as_deref().unwrap_or("");
        let machine_path = machine_path(
            session.app_hub_group.as_deref(),
            session.manager.as_deref(),
            &session.machine_id,
        );
        let prefix = if group.is_empty() {
            String::new()
        } else {
            format!("{group}|")
        };
        let row_id = if session.manager.as_deref().is_some_and(|manager| !manager.is_empty()) {
            format!("{}|{}", machine_path, session.session_id)
        } else {
            format!("{prefix}{short_id}")
        };
        // iOS compares the complete SessionKey.path; a hub-less short id must not hold a same-named row in another hub.
        let held = input.settings.held.as_deref() == Some(row_id.as_str());
        let sort_date = if state == AgentState::Working {
            session.state_since.as_deref().or(session.updated_at.as_deref())
        } else {
            session.updated_at.as_deref()
        };
        Self {
            id: row_id,
            short_id,
            machine_id: session.machine_id.clone(),
            session_id: session.session_id.clone(),
            machine_path,
            group: group.to_owned(),
            manager: session.manager.clone(),
            state,
            listed: state.is_live() || (input.settings.history && session.archived),
            archived: session.archived,
            sort_date: sort_date.and_then(utc_timestamp),
            is_waiting: state.is_quiet()
                && (session.is_waiting()
                    || session
                        .children
                        .as_ref()
                        .is_some_and(|children| children.working > 0 || children.needs_you > 0 || children.sides > 0)),
            background_waiting: state == AgentState::Working
                && session.background_waiting
                && session
                    .children
                    .as_ref()
                    .is_none_or(|children| children.working == 0 && children.needs_you == 0),
            nested_subtask: session.nested_subtask,
            space_kind: session.space_kind.clone().unwrap_or_default(),
            pane_id: session.pane_id.clone(),
            pane_index: session.pane_index,
            pinned,
            held,
            muted,
            awaits_reply: session.awaits_reply,
            just_finished: session.work.as_ref().is_some_and(|work| work.just_finished),
            space_id: session.space_id.clone().unwrap_or_else(|| "-".to_owned()),
            space_name,
            slot_index: session.slot_index,
            slot_id: session.slot_id.clone().unwrap_or_default(),
        }
    }

    fn stays(&self) -> bool {
        !self.state.is_quiet()
            || self.pinned
            || self.held
            || self.is_waiting
            || self.awaits_reply()
            || self.just_finished()
            || self.archived
    }

    fn awaits_reply(&self) -> bool {
        self.state == AgentState::Idle && self.awaits_reply && !self.muted
    }

    fn just_finished(&self) -> bool {
        self.state == AgentState::Idle && self.just_finished && !self.muted
    }

    fn counted_state(&self) -> AgentState {
        if self.background_waiting {
            AgentState::Idle
        } else {
            self.state
        }
    }

    /// The index into the nine By Status sections. Needs You never drops because of a mute.
    /// A held agent that has become quiet keeps its Unread rank, as iOS's `rank` does (#2213).
    fn rank(&self) -> usize {
        if self.state == AgentState::NeedsYou {
            return 0;
        }
        let unread = self.state == AgentState::Unread || (self.held && self.state.is_quiet());
        let live = self.state == AgentState::Working || unread || self.is_waiting;
        if live && self.muted {
            return 7;
        }
        if unread {
            return 1;
        }
        if self.awaits_reply() {
            return 2;
        }
        match self.state {
            AgentState::Working if self.background_waiting => 4,
            AgentState::Working => 3,
            _ if self.is_waiting => 6,
            _ if self.just_finished() => 5,
            _ => 8,
        }
    }
}

#[derive(Default)]
struct Query {
    words: Vec<String>,
    machines: Vec<String>,
    projects: Vec<String>,
    harnesses: Vec<String>,
    models: Vec<String>,
    states: Vec<String>,
    pinned_only: bool,
    errors_only: bool,
    /// Parsed `since:` window in hours, matching iOS's `AgentParsedQuery.sinceHours`.
    since_hours: Option<f64>,
}

impl Query {
    fn parse(raw: &str) -> Self {
        let mut query = Self::default();
        for token in raw.split_whitespace() {
            let Some((key, value)) = token.split_once(':') else {
                query.words.push(token.to_lowercase());
                continue;
            };
            if key.is_empty() || value.is_empty() {
                query.words.push(token.to_lowercase());
                continue;
            }
            let value = value.to_lowercase();
            match key.to_lowercase().as_str() {
                "machine" | "host" => query.machines.push(value),
                "project" | "repo" | "cwd" | "path" => query.projects.push(value),
                "agent" | "harness" | "launcher" => query.harnesses.push(value),
                "model" => query.models.push(value),
                "state" | "status" => query.states.push(value),
                "is" if matches!(value.as_str(), "pinned" | "pin") => query.pinned_only = true,
                "is" if matches!(value.as_str(), "error" | "errors" | "failed" | "blocked") => {
                    query.errors_only = true;
                }
                "is" => query.states.push(value),
                "since" | "after" | "updated"
                    if matches!(value.as_str(), "24h" | "1d" | "48h" | "2d" | "7d" | "1w" | "30d" | "1m") =>
                {
                    let hours: f64 = match value.as_str() {
                        "24h" | "1d" | "48h" | "2d" => 24.0,
                        "7d" | "1w" => 168.0,
                        "30d" | "1m" => 720.0,
                        _ => unreachable!(),
                    };
                    query.since_hours = Some(query.since_hours.map_or(hours, |prev: f64| prev.min(hours)));
                }
                _ => query.words.push(token.to_lowercase()),
            }
        }
        query
    }

    fn compile(settings: &Settings) -> Self {
        let mut query = Self::parse(&settings.query);
        if !settings.machine_title_filter.is_empty() {
            query.machines.push(settings.machine_title_filter.to_lowercase());
        }
        if !settings.project_filter.is_empty() {
            query.projects.push(settings.project_filter.to_lowercase());
        }
        if !settings.harness_filter.is_empty() {
            query.harnesses.push(settings.harness_filter.to_lowercase());
        }
        if !settings.model_filter.is_empty() {
            query.models.push(settings.model_filter.to_lowercase());
        }
        query.pinned_only |= settings.pinned_only;
        query.errors_only |= settings.search_scope == SearchScope::Errors;
        if settings.search_as_code {
            let raw = settings.query.to_lowercase();
            // i18n-ignore: raw query words matched by iOS's Search as Code heuristics.
            if ["报错", "错误", "失败", "阻塞", "error", "failed"]
                .into_iter()
                .any(|word| raw.contains(word))
            {
                query.errors_only = true;
            }
            // i18n-ignore: raw query words matched by iOS's Search as Code heuristics.
            if raw.contains("置顶") || raw.contains("pinned") {
                query.pinned_only = true;
            }
            // i18n-ignore: raw query words matched by iOS's Search as Code heuristics.
            if query.since_hours.is_none() && (raw.contains("最近两天") || raw.contains("今天") || raw.contains("24h"))
            {
                query.since_hours = Some(48.0);
            }
            // i18n-ignore: raw query words matched by iOS's Search as Code heuristics.
            if query.since_hours.is_none() && (raw.contains("最近一周") || raw.contains("本周") || raw.contains("7d"))
            {
                query.since_hours = Some(168.0);
            }
        }
        query
    }

    fn matches(&self, session: &Session, input: &Input) -> bool {
        self.matches_one(session, input)
            || session
                .search_subtasks
                .iter()
                .any(|subtask| self.matches(subtask, input))
    }

    fn matches_one(&self, session: &Session, input: &Input) -> bool {
        if session.assistant {
            return false;
        }
        let machine = input.machines.iter().find(|machine| {
            machine.id == session.machine_id
                && machine.group.as_deref().unwrap_or("") == session.app_hub_group.as_deref().unwrap_or("")
                && machine.manager.as_deref() == session.manager.as_deref()
        });
        let machine_name = machine.map_or("", |machine| machine.name.as_str()).to_lowercase();
        let machine_path = machine_path(
            session.app_hub_group.as_deref(),
            session.manager.as_deref(),
            &session.machine_id,
        );
        if !input.settings.machine_filter.is_empty() && machine_path != input.settings.machine_filter {
            let legacy = session
                .app_hub_group
                .as_deref()
                .map_or(session.machine_id.clone(), |group| {
                    if group.is_empty() {
                        session.machine_id.clone()
                    } else {
                        format!("{group}|{}", session.machine_id)
                    }
                });
            if legacy != input.settings.machine_filter {
                return false;
            }
        }
        if input.settings.multi_hub
            && !input.settings.hub_filter.is_empty()
            && input.settings.hub_filter.strip_prefix('=') != Some(session.app_hub_group.as_deref().unwrap_or(""))
        {
            return false;
        }
        if session.nested_match {
            // A child admitted the query for this prepared root. Query predicates belong to that child; the ordinary
            // parent still had to pass the assistant and selected-machine/hub scopes above.
            return true;
        }
        let machine_path = machine_path.to_lowercase();
        if !self
            .machines
            .iter()
            .all(|word| machine_path.contains(word) || machine_name.contains(word))
        {
            return false;
        }
        if self.pinned_only && !input.pinned(session) {
            return false;
        }
        if self.errors_only && !has_error(session) {
            return false;
        }
        let project = session
            .repo_root
            .as_deref()
            .filter(|root| !root.is_empty())
            .map(folder_name)
            .or_else(|| session.space_name.clone().filter(|name| !name.trim().is_empty()))
            .unwrap_or_else(|| folder_name(&session.cwd))
            .to_lowercase();
        let cwd = session.cwd.to_lowercase();
        if !self
            .projects
            .iter()
            .all(|word| project.contains(word) || cwd.contains(word))
        {
            return false;
        }
        let harness = session.harness_id().to_lowercase();
        let agent_label = session.agent_label().to_lowercase();
        if !self
            .harnesses
            .iter()
            .all(|word| harness.contains(word) || agent_label.contains(word))
        {
            return false;
        }
        let model = format!(
            "{} {}",
            session.model.as_deref().unwrap_or(""),
            session.launcher.as_deref().unwrap_or("")
        )
        .to_lowercase();
        if !self.models.iter().all(|word| model.contains(word)) {
            return false;
        }
        let state = AgentState::from_client(session.state.as_deref(), session.status.as_deref());
        if !self.states.is_empty() && !self.states.contains(&state.as_str().to_owned()) {
            return false;
        }
        if let Some(hours) = self.since_hours {
            // iOS: `guard let updated = session.updatedAt, Date.now.timeIntervalSince(updated) <= hrs * 3600 else { return false }`.
            // The caller supplies `now`; the projection never reads a clock.
            let Some(now) = input.settings.now.as_deref().and_then(utc_timestamp) else {
                return false;
            };
            let Some(updated) = session.updated_at.as_deref().and_then(utc_timestamp) else {
                return false;
            };
            let elapsed_seconds = now.saturating_sub(updated) as f64 / 1_000_000_000.0;
            if elapsed_seconds > hours * 3600.0 {
                return false;
            }
        }
        if self.words.is_empty() {
            return true;
        }
        let agent_label = session.agent_label();
        let prose = session
            .last_text
            .as_deref()
            .unwrap_or("")
            .lines()
            .filter(|line| {
                let trimmed = line.trim();
                !trimmed.starts_with("$ ")
                    && !trimmed.starts_with("Running ")
                    && !trimmed.starts_with("Tool:")
                    && !trimmed.starts_with("tool_use")
            })
            .collect::<Vec<_>>()
            .join(" ");
        let work_line = session
            .work
            .as_ref()
            .and_then(|work| work.line.as_deref())
            .unwrap_or("");
        let goal = session.goal.as_ref().map_or("", |goal| goal.objective.as_str());
        let todo = session.todo.as_ref().map_or("", |todo| todo.title.as_str());
        let fields: Vec<&str> = match input.settings.search_scope {
            SearchScope::All => vec![
                session.title.as_str(),
                session.cwd.as_str(),
                session.repo_root.as_deref().unwrap_or(""),
                session.branch.as_deref().unwrap_or(""),
                session.last_text.as_deref().unwrap_or(""),
                session.asks.as_deref().unwrap_or(""),
                session.space_name.as_deref().unwrap_or(""),
                session.slot_name.as_deref().unwrap_or(""),
                agent_label.as_str(),
                session.model.as_deref().unwrap_or(""),
                session.launcher.as_deref().unwrap_or(""),
                session.start_error.as_deref().unwrap_or(""),
                session.turn_error.as_deref().unwrap_or(""),
            ],
            SearchScope::Title => vec![
                session.title.as_str(),
                session.space_name.as_deref().unwrap_or(""),
                session.slot_name.as_deref().unwrap_or(""),
            ],
            SearchScope::Conversation => vec![
                session.title.as_str(),
                session.asks.as_deref().unwrap_or(""),
                goal,
                prose.as_str(),
                work_line,
            ],
            SearchScope::User => vec![
                session.asks.as_deref().unwrap_or(""),
                goal,
                todo,
                session.title.as_str(),
            ],
            SearchScope::Assistant => vec![prose.as_str(), work_line],
            SearchScope::Errors => vec![
                session.start_error.as_deref().unwrap_or(""),
                session.turn_error.as_deref().unwrap_or(""),
                session.failure_reason.as_deref().unwrap_or(""),
                session.attention_reason.as_deref().unwrap_or(""),
                work_line,
                session.title.as_str(),
            ],
        };
        self.words
            .iter()
            .all(|word| fields.iter().any(|field| field.to_lowercase().contains(word)))
    }
}

fn has_error(session: &Session) -> bool {
    [
        session.start_error.as_deref(),
        session.turn_error.as_deref(),
        session.failure_reason.as_deref(),
        session.attention_reason.as_deref(),
    ]
    .into_iter()
    .flatten()
    .any(|text| !text.is_empty())
        || session.work.as_ref().is_some_and(|work| work.phase == "blocked")
        || AgentState::from_client(session.state.as_deref(), session.status.as_deref()) == AgentState::NeedsYou
}

/// Build one immutable Agents list. The expected fixture objects are compared as complete JSON values by its test.
pub fn project(input: &Input) -> Projection {
    project_impl(input, false)
}

fn project_impl(input: &Input, oldest_first: bool) -> Projection {
    let query = Query::compile(&input.settings);
    let base: Vec<Row> = input
        .sessions
        .iter()
        .filter(|session| query.matches(session, input))
        .cloned()
        .map(|session| Row::new(session, input))
        .collect();
    let counts = counts(&base);
    let quick = input.settings.quick;
    let matching: HashSet<&str> = if quick == Quick::All {
        HashSet::new()
    } else {
        base.iter()
            .filter(|row| quick_matches(row, quick))
            .map(|row| row.id.as_str())
            .collect()
    };
    let kept: HashSet<&str> = input.settings.kept.iter().map(String::as_str).collect();
    let filtered: Vec<&Row> = base
        .iter()
        .filter(|row| quick == Quick::All || matching.contains(row.id.as_str()) || kept.contains(row.id.as_str()))
        .collect();
    let filtered_ids = input
        .settings
        .inventory
        .then(|| filtered.iter().map(|row| row.id.clone()).collect());
    let unread_shown = filtered
        .iter()
        .filter(|row| row.state == AgentState::Unread)
        .map(|row| row.id.clone())
        .collect();
    let mut groups = Vec::new();
    let mut quiet_title = None;
    let pinned = pinned_sessions(&filtered, input);
    if !pinned.is_empty() {
        groups.push(Group {
            id: "pinned".to_owned(),
            title: "置顶".to_owned(), // i18n-ignore
            rows: pinned.iter().map(|row| row.id.clone()).collect(),
            visible_rows: pinned.iter().map(|row| row.id.clone()).collect(),
            folded: false,
            parent: None,
            state_counts: None,
            spoken: None,
        });
    }
    let unpinned: Vec<&Row> = filtered.into_iter().filter(|row| !row.pinned).collect();
    let unpinned_ids = input
        .settings
        .inventory
        .then(|| unpinned.iter().map(|row| row.id.clone()).collect());
    match input.settings.grouping {
        Grouping::State => groups.extend(state_groups(
            &unpinned,
            quick,
            &input.settings,
            input.settings.sort != "updated",
            oldest_first,
        )),
        Grouping::Machine => groups.extend(machine_groups(
            &unpinned,
            input,
            quick,
            input.settings.sort != "updated",
        )),
        Grouping::Space => {
            let (mut spaces, quiet) = space_groups(&unpinned, input, quick, input.settings.sort == "updated");
            groups.append(&mut spaces);
            if quiet {
                quiet_title = Some("空闲的空间".to_owned()); // i18n-ignore
            }
        }
    }
    let rows = (input.settings.grouping == Grouping::Space && input.settings.presentation_rows)
        .then(|| tab_rows(&unpinned, &groups, &input.settings));
    Projection {
        counts,
        groups,
        rows,
        unread_shown,
        filtered: filtered_ids,
        unpinned: unpinned_ids,
        quiet_title: (input.settings.grouping == Grouping::Space).then_some(quiet_title),
    }
}

/// Mutation-probe entry point used by the integration test, not by the normal projection.
#[doc(hidden)]
pub fn project_with_order(input: &Input) -> Projection {
    project_impl(input, true)
}

fn counts(base: &[Row]) -> Counts {
    let mut counts = Counts::default();
    for row in base.iter().filter(|row| !row.nested_subtask) {
        match row.counted_state() {
            // ChatsView.count(_:) counts by countedState without excluding mutes (the filter bar's
            // `filterCounts` is a separate predicate, AgentFilter.matches, which does exclude them).
            AgentState::NeedsYou => counts.needs_you += 1,
            AgentState::Unread => counts.unread += 1,
            AgentState::Working => counts.working += 1,
            _ => {}
        }
        if row.background_waiting && !row.muted {
            counts.background_waiting += 1;
        }
    }
    counts
}

fn quick_matches(row: &Row, quick: Quick) -> bool {
    match quick {
        Quick::All => true,
        Quick::NeedsYou => row.state == AgentState::NeedsYou,
        Quick::Unread => row.counted_state() == AgentState::Unread && !row.muted,
        Quick::Working => row.counted_state() == AgentState::Working && !row.muted,
        Quick::BackgroundWaiting => row.background_waiting && !row.muted,
    }
}

fn ordered<'a>(rows: impl IntoIterator<Item = &'a Row>, by_rank: bool) -> Vec<&'a Row> {
    ordered_with(rows, by_rank, false)
}

/// `oldest_first` exists only to exercise a wrong comparison in the fixture mutation test.
fn ordered_with<'a>(rows: impl IntoIterator<Item = &'a Row>, by_rank: bool, oldest_first: bool) -> Vec<&'a Row> {
    let mut rows: Vec<&Row> = rows.into_iter().collect();
    rows.sort_by(|a, b| {
        let rank = if by_rank {
            a.rank().cmp(&b.rank())
        } else {
            Ordering::Equal
        };
        let dates = if oldest_first {
            a.sort_date.cmp(&b.sort_date)
        } else {
            b.sort_date.cmp(&a.sort_date)
        };
        rank.then_with(|| dates)
            .then_with(|| a.session_id.cmp(&b.session_id))
            .then_with(|| a.id.cmp(&b.id))
    });
    rows
}

fn pinned_sessions<'a>(rows: &[&'a Row], input: &Input) -> Vec<&'a Row> {
    let mut pinned: Vec<&Row> = rows.iter().copied().filter(|row| row.pinned).collect();
    pinned.sort_by_key(|row| {
        let (pins, _) = input.preferences(&row.group, row.manager.as_deref());
        pins.sessions
            .iter()
            .position(|id| *id == row.short_id || *id == row.id)
            .unwrap_or(0)
    });
    pinned
}

const STATE_SECTIONS: [(&str, &str); 9] = [
    ("needs", "需要你"),             // i18n-ignore
    ("unread", "完成未看"),          // i18n-ignore
    ("awaiting", "待你回复"),        // i18n-ignore
    ("working", "工作中"),           // i18n-ignore
    ("background", "后台等待中"),    // i18n-ignore
    ("just_finished", "刚做完待看"), // i18n-ignore
    ("waiting", "待唤醒"),           // i18n-ignore
    ("muted", "已屏蔽"),             // i18n-ignore
    ("idle", "已看"),                // i18n-ignore
];

fn state_groups(rows: &[&Row], quick: Quick, settings: &Settings, by_rank: bool, oldest_first: bool) -> Vec<Group> {
    let live: Vec<&Row> = rows.iter().copied().filter(|row| row.listed).collect();
    STATE_SECTIONS
        .iter()
        .enumerate()
        .filter(|(rank, _)| {
            // Show every section that has rows, including kept rows that a quick filter retained.
            // iOS's stateSections shows every nonempty section; the quick filter narrows `filtered`,
            // not which sections appear.
            live.iter().any(|row| row.rank() == *rank)
        })
        .map(|(rank, (key, title))| {
            let id = format!("state/{key}");
            let members = ordered_with(
                live.iter().copied().filter(|row| row.rank() == rank),
                quick == Quick::All && by_rank,
                oldest_first,
            );
            let folded = is_folded(&id, settings);
            plain_group(id, (*title).to_owned(), members, folded, quick, settings, None)
        })
        .collect()
}

fn machine_groups(rows: &[&Row], input: &Input, quick: Quick, by_rank: bool) -> Vec<Group> {
    let recent_sort = input.settings.sort == "updated";
    let mut machines_with_rows: Vec<(&Machine, Vec<&Row>)> = input
        .machines
        .iter()
        .filter_map(|machine| {
            let path = machine.path();
            let members: Vec<&Row> = rows
                .iter()
                .copied()
                .filter(|row| row.machine_path == path && row.listed)
                .collect();
            if members.is_empty() {
                return None;
            }
            Some((machine, members))
        })
        .collect();
    // Under Recently Updated, machines sort by their newest session's sortDate, then by path.
    if recent_sort {
        machines_with_rows.sort_by(|(a, a_rows), (b, b_rows)| {
            let a_date = a_rows.iter().filter_map(|r| r.sort_date).max();
            let b_date = b_rows.iter().filter_map(|r| r.sort_date).max();
            b_date.cmp(&a_date).then_with(|| a.path().cmp(&b.path()))
        });
    }
    machines_with_rows
        .into_iter()
        .map(|(machine, members)| {
            let path = machine.path();
            let members = ordered(members, quick == Quick::All && by_rank);
            let id = format!("machine/{path}");
            plain_group(
                id,
                machine.name.clone(),
                members,
                is_folded(&format!("machine/{path}"), &input.settings),
                quick,
                &input.settings,
                Some(format!("machine-{path}")),
            )
        })
        .collect()
}

fn plain_group(
    id: String,
    title: String,
    members: Vec<&Row>,
    folded: bool,
    quick: Quick,
    settings: &Settings,
    idle_key: Option<String>,
) -> Group {
    let visible_rows = if folded {
        Vec::new()
    } else if quick != Quick::All {
        ids(&members)
    } else {
        let expand = idle_key
            .as_ref()
            .is_some_and(|key| settings.expanded_idle.contains(key))
            || !settings.query.is_empty();
        members
            .iter()
            .filter(|row| row.stays() || expand || idle_key.is_none())
            .map(|row| row.id.clone())
            .collect()
    };
    Group {
        id,
        title,
        rows: ids(&members),
        visible_rows,
        folded,
        parent: None,
        state_counts: None,
        spoken: None,
    }
}

struct Space<'a> {
    id: String,
    machine_id: String,
    group: String,
    manager: Option<String>,
    name: String,
    members: Vec<&'a Row>,
    priority: u8,
    sort_date: Option<i128>,
    quiet: bool,
    pinned: bool,
    muted: bool,
}

fn space_groups(rows: &[&Row], input: &Input, quick: Quick, recent_sort: bool) -> (Vec<Group>, bool) {
    let mut spaces: BTreeMap<String, Space> = BTreeMap::new();
    for row in rows.iter().copied().filter(|row| row.listed) {
        let machine_path = row.machine_path.clone();
        let space = spaces
            .entry(format!("{}/{}", machine_path, row.space_id))
            .or_insert_with(|| Space {
                id: format!("{}/{}", machine_path, row.space_id),
                machine_id: row.machine_id.clone(),
                group: row.group.clone(),
                manager: row.manager.clone(),
                name: row.space_name.clone(),
                members: Vec::new(),
                priority: u8::MAX,
                sort_date: None,
                quiet: true,
                pinned: false,
                muted: false,
            });
        space.priority = space.priority.min(priority(row.counted_state()));
        space.sort_date = space.sort_date.max(row.sort_date);
        space.quiet &=
            row.state.is_quiet() && !row.is_waiting && !row.awaits_reply() && !row.just_finished() && !row.held;
        space.members.push(row);
    }
    let mut spaces: Vec<Space> = spaces.into_values().collect();
    for space in &mut spaces {
        space.pinned = input.pinned_space(space);
        space.muted = input.muted_space(space);
        space.members.sort_by(|a, b| {
            a.slot_index
                .unwrap_or(0)
                .cmp(&b.slot_index.unwrap_or(0))
                .then_with(|| a.slot_id.cmp(&b.slot_id))
                .then_with(|| a.session_id.cmp(&b.session_id))
                .then_with(|| a.id.cmp(&b.id))
        });
    }
    spaces.sort_by(|a, b| {
        if !recent_sort {
            a.priority.cmp(&b.priority)
        } else {
            Ordering::Equal
        }
        .then_with(|| b.sort_date.cmp(&a.sort_date))
        .then_with(|| a.id.cmp(&b.id))
    });
    fn by_time(mut items: Vec<Space<'_>>) -> Vec<Space<'_>> {
        items.sort_by(|a: &Space, b: &Space| b.sort_date.cmp(&a.sort_date).then_with(|| a.id.cmp(&b.id)));
        items
    }
    let pinned = |space: &&Space| space.pinned && !space.muted;
    let (active, quiet): (Vec<Space>, Vec<Space>) = if quick == Quick::All && !recent_sort {
        let mut pinned_spaces: Vec<Space> = Vec::new();
        let mut busy = Vec::new();
        let mut muted = Vec::new();
        let mut quiet = Vec::new();
        for space in spaces {
            if pinned(&&space) {
                pinned_spaces.push(space);
            } else if space.quiet {
                quiet.push(space);
            } else if space.muted {
                muted.push(space);
            } else {
                busy.push(space);
            }
        }
        pinned_spaces.extend(busy);
        pinned_spaces.extend(muted);
        (pinned_spaces, quiet)
    } else {
        let mut active = Vec::new();
        let mut rest = Vec::new();
        for space in spaces {
            if pinned(&&space) {
                active.push(space);
            } else {
                rest.push(space);
            }
        }
        active.extend(by_time(rest));
        (active, Vec::new())
    };
    let mut groups: Vec<Group> = active
        .into_iter()
        .map(|space| make_space(space, false, input, quick, recent_sort))
        .collect();
    let has_quiet = !quiet.is_empty();
    groups.extend(
        quiet
            .into_iter()
            .map(|space| make_space(space, true, input, quick, recent_sort)),
    );
    (groups, has_quiet)
}

fn make_space(space: Space, quiet: bool, input: &Input, quick: Quick, by_time: bool) -> Group {
    let folded = if quiet {
        !input.settings.expanded_quiet.contains(&space.id) && input.settings.query.is_empty()
    } else {
        is_folded(&space.id, &input.settings)
    };
    let visible_rows = if folded {
        Vec::new()
    } else if quiet {
        ids(&ordered(space.members.iter().copied(), quick == Quick::All && !by_time))
    } else if input.settings.presentation_rows {
        let expand = input.settings.expanded_idle.contains(&space.id) || !input.settings.query.is_empty();
        let mut by_tab: BTreeMap<String, Vec<&Row>> = BTreeMap::new();
        for row in &space.members {
            if let Some(key) = tab_key(row) {
                by_tab.entry(key).or_default().push(row);
            }
        }
        let mut visible = Vec::new();
        let mut placed = BTreeSet::new();
        for row in ordered(space.members.iter().copied(), quick == Quick::All && !by_time) {
            let Some(key) = tab_key(row) else {
                if quick != Quick::All || row.stays() || expand {
                    visible.push(row.id.clone());
                }
                continue;
            };
            if !placed.insert(key.clone()) {
                continue;
            }
            let panes = by_tab.get(&key).into_iter().flatten().copied().collect::<Vec<_>>();
            if panes.len() < 2 {
                if quick != Quick::All || row.stays() || expand {
                    visible.push(row.id.clone());
                }
            } else if quick != Quick::All || panes.iter().any(|pane| pane.stays()) || expand {
                let mut panes = panes.clone();
                panes.sort_by(|a, b| {
                    a.pane_index
                        .unwrap_or(i64::MAX)
                        .cmp(&b.pane_index.unwrap_or(i64::MAX))
                        .then_with(|| pane_number(a).cmp(&pane_number(b)))
                        .then_with(|| a.session_id.cmp(&b.session_id))
                });
                visible.extend(ids(&panes));
            }
        }
        visible
    } else {
        let members = ordered(space.members.iter().copied(), quick == Quick::All && !by_time);
        let expand = input.settings.expanded_idle.contains(&space.id) || !input.settings.query.is_empty();
        members
            .iter()
            .filter(|row| quick != Quick::All || row.stays() || expand)
            .map(|row| row.id.clone())
            .collect()
    };
    let (state_counts, background_waiting) = if input.settings.bounded_spaces.contains(&space.id) {
        let aggregate = input.settings.aggregate.get(&space.id);
        let counts = StateCounts {
            needs_you: aggregate
                .and_then(|counts| counts.state.get("needs_you").copied())
                .unwrap_or(0),
            working: aggregate
                .and_then(|counts| counts.state.get("working").copied())
                .unwrap_or(0),
            unread: aggregate
                .and_then(|counts| counts.state.get("unread").copied())
                .unwrap_or(0),
            idle: aggregate
                .and_then(|counts| counts.state.get("idle").copied())
                .unwrap_or(0),
            unknown: aggregate
                .and_then(|counts| counts.state.get("unknown").copied())
                .unwrap_or(0),
        };
        // Hub state totals cover background waiting as working; the immutable rows carry the finer-grained flag.
        (
            counts,
            space
                .members
                .iter()
                .filter(|row| row.background_waiting && !row.nested_subtask)
                .count(),
        )
    } else {
        input.settings.aggregate.get(&space.id).map_or_else(
            || {
                (
                    state_counts(&space.members),
                    space
                        .members
                        .iter()
                        .filter(|row| row.background_waiting && !row.nested_subtask)
                        .count(),
                )
            },
            |aggregate| {
                let counts = StateCounts {
                    needs_you: aggregate.state.get("needs_you").copied().unwrap_or(0),
                    working: aggregate.state.get("working").copied().unwrap_or(0),
                    unread: aggregate.state.get("unread").copied().unwrap_or(0),
                    idle: aggregate.state.get("idle").copied().unwrap_or(0),
                    unknown: aggregate.state.get("unknown").copied().unwrap_or(0),
                };
                // The hub supplies state totals only. Waiting remains a property of the immutable session rows.
                (
                    counts,
                    space
                        .members
                        .iter()
                        .filter(|row| row.background_waiting && !row.nested_subtask)
                        .count(),
                )
            },
        )
    };
    let rows = if quiet {
        ids(&ordered(space.members.iter().copied(), quick == Quick::All && !by_time))
    } else if input.settings.presentation_rows {
        let mut by_tab: BTreeMap<String, Vec<&Row>> = BTreeMap::new();
        for row in &space.members {
            if let Some(key) = tab_key(row) {
                by_tab.entry(key).or_default().push(row);
            }
        }
        let mut output = Vec::with_capacity(space.members.len());
        let mut placed = BTreeSet::new();
        for row in ordered(space.members.iter().copied(), quick == Quick::All && !by_time) {
            let Some(key) = tab_key(row) else {
                output.push(row);
                continue;
            };
            let Some(panes) = by_tab.get(&key) else { continue };
            if panes.len() < 2 {
                output.push(row);
                continue;
            }
            if !placed.insert(key) {
                continue;
            }
            let mut panes = panes.clone();
            panes.sort_by(|a, b| {
                a.pane_index
                    .unwrap_or(i64::MAX)
                    .cmp(&b.pane_index.unwrap_or(i64::MAX))
                    .then_with(|| pane_number(a).cmp(&pane_number(b)))
                    .then_with(|| a.session_id.cmp(&b.session_id))
            });
            output.extend(panes);
        }
        ids(&output)
    } else {
        ids(&ordered(space.members.iter().copied(), quick == Quick::All && !by_time))
    };
    Group {
        id: space.id,
        title: space.name,
        rows,
        visible_rows,
        folded,
        parent: quiet.then(|| "quiet".to_owned()),
        spoken: Some(spoken(&state_counts, background_waiting)),
        state_counts: Some(state_counts),
    }
}

fn tab_key(row: &Row) -> Option<String> {
    if row.slot_id.is_empty() || row.space_id == "-" || row.space_id.is_empty() {
        None
    } else {
        Some(format!(
            "{}/{}:{}/{}",
            row.machine_path, row.space_kind, row.space_id, row.slot_id
        ))
    }
}

/// The rows the compact By Project view actually draws. A quiet space opens flat pane rows, not tab headers.
fn tab_rows(rows: &[&Row], groups: &[Group], settings: &Settings) -> Vec<Entry> {
    let pinned: Vec<&Row> = rows.iter().copied().filter(|row| row.pinned).collect();
    let by_id: StdHashMap<&str, &Row> = rows.iter().copied().map(|row| (row.id.as_str(), row)).collect();
    let group_members = |group: &Group| {
        group
            .rows
            .iter()
            .filter_map(|id| by_id.get(id.as_str()).copied())
            .collect::<Vec<&Row>>()
    };
    // make_space already owns display order; unread_shown separately retains filtered input order.
    let mut entries = plain_entries(&pinned);
    for group in groups.iter().filter(|group| group.id != "pinned") {
        if group.folded {
            continue;
        }
        let members = group_members(group);
        let expanded = settings.expanded_idle.contains(&group.id) || !settings.query.is_empty();
        let busy: BTreeSet<&str> = members
            .iter()
            .filter(|row| row.stays())
            .map(|row| row.id.as_str())
            .collect();
        if group.parent.as_deref() == Some("quiet") {
            let mut ordered = members;
            ordered.sort_by(|a, b| {
                a.slot_index
                    .unwrap_or(0)
                    .cmp(&b.slot_index.unwrap_or(0))
                    .then_with(|| a.slot_id.cmp(&b.slot_id))
                    .then_with(|| a.pane_index.unwrap_or(i64::MAX).cmp(&b.pane_index.unwrap_or(i64::MAX)))
                    .then_with(|| pane_number(a).cmp(&pane_number(b)))
                    .then_with(|| a.session_id.cmp(&b.session_id))
            });
            entries.extend(plain_entries(&ordered));
            continue;
        }
        let mut owned_tabs: BTreeMap<String, Vec<&Row>> = BTreeMap::new();
        for row in &members {
            if let Some(key) = tab_key(row) {
                owned_tabs.entry(key).or_default().push(row);
            }
        }
        let mut placed: BTreeSet<String> = BTreeSet::new();
        for row in &members {
            if !busy.contains(row.id.as_str()) && !expanded && settings.quick == Quick::All {
                continue;
            }
            let Some(key) = tab_key(row) else {
                if let Some(entry) = plain_entries(&[row]).into_iter().next() {
                    entries.push(entry);
                }
                continue;
            };
            let Some(panes) = owned_tabs.get(&key) else { continue };
            if panes.len() < 2 {
                if let Some(entry) = plain_entries(&[row]).into_iter().next() {
                    entries.push(entry);
                }
                continue;
            }
            if !placed.insert(key.clone()) {
                continue;
            }
            let mut panes = panes.clone();
            panes.sort_by(|a, b| {
                a.pane_index
                    .unwrap_or(i64::MAX)
                    .cmp(&b.pane_index.unwrap_or(i64::MAX))
                    .then_with(|| pane_number(a).cmp(&pane_number(b)))
                    .then_with(|| a.session_id.cmp(&b.session_id))
            });
            let fold_id = tab_fold_id(row);
            let folded = settings.query.is_empty() && settings.folded.contains(&fold_id);
            entries.push(Entry {
                id: format!("tab|{key}"),
                fold_id,
                rows: ids(&panes),
                visible_rows: if folded { Vec::new() } else { ids(&panes) },
                folded,
            });
        }
    }
    entries
}

fn plain_entries(rows: &[&Row]) -> Vec<Entry> {
    rows.iter()
        .map(|row| Entry {
            id: row.id.clone(),
            fold_id: row.id.clone(),
            rows: vec![row.id.clone()],
            visible_rows: vec![row.id.clone()],
            folded: false,
        })
        .collect()
}

fn tab_fold_id(row: &Row) -> String {
    let slot = row
        .slot_index
        .map_or_else(|| row.slot_id.clone(), |index| index.to_string());
    format!("tab/{}/{}/{}", row.machine_path, row.space_name, slot)
}

fn pane_number(row: &Row) -> Option<i64> {
    let id = row.pane_id.as_deref().filter(|id| !id.is_empty())?;
    let suffix = id.rsplit_once(':').map_or(id, |(_, suffix)| suffix);
    if let Some(number) = suffix.strip_prefix('p')
        && !number.is_empty()
        && number.bytes().all(|byte| byte.is_ascii_alphanumeric())
        && let Ok(value) = i64::from_str_radix(number, 36)
    {
        return Some(value);
    }
    let digits: String = suffix
        .chars()
        .rev()
        .take_while(|character| character.is_ascii_digit())
        .collect();
    if digits.is_empty() {
        None
    } else {
        digits.chars().rev().collect::<String>().parse().ok()
    }
}

fn state_counts(rows: &[&Row]) -> StateCounts {
    let mut counts = StateCounts::default();
    for row in rows.iter().filter(|row| !row.nested_subtask) {
        match row.counted_state() {
            AgentState::NeedsYou => counts.needs_you += 1,
            AgentState::Working => counts.working += 1,
            AgentState::Unread => counts.unread += 1,
            AgentState::Idle => counts.idle += 1,
            AgentState::Unknown => counts.unknown += 1,
            AgentState::Offline => {}
        }
    }
    counts
}

fn spoken(counts: &StateCounts, background_waiting: usize) -> String {
    let mut phrases = Vec::new();
    for (count, label) in [
        (counts.needs_you, "需要你"),       // i18n-ignore
        (counts.working, "工作中"),         // i18n-ignore
        (counts.unread, "完成未看"),        // i18n-ignore
        (background_waiting, "后台等待中"), // i18n-ignore
        (
            (counts.idle + counts.unknown).saturating_sub(background_waiting),
            "空闲", // i18n-ignore
        ),
    ] {
        if count > 0 {
            phrases.push(format!("{count} 个{label}")); // i18n-ignore
        }
    }
    phrases.join("，") // i18n-ignore
}

fn priority(state: AgentState) -> u8 {
    match state {
        AgentState::NeedsYou => 0,
        AgentState::Unread => 1,
        AgentState::Working => 2,
        AgentState::Idle => 3,
        AgentState::Unknown => 4,
        AgentState::Offline => 5,
    }
}

fn is_folded(id: &str, settings: &Settings) -> bool {
    if !settings.query.is_empty() {
        return false;
    }
    let folded: BTreeSet<&str> = settings.folded.iter().map(String::as_str).collect();
    if id == "state/idle" {
        !folded.contains("!state/idle")
    } else {
        folded.contains(id)
    }
}

fn ids(rows: &[&Row]) -> Vec<String> {
    rows.iter().map(|row| row.id.clone()).collect()
}

fn folder_name(path: &str) -> String {
    path.trim_end_matches('/').rsplit('/').next().unwrap_or(path).to_owned()
}
