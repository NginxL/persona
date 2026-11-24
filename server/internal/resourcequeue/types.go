// Package resourcequeue defines the shared runner protocol, without hub or database dependencies.
package resourcequeue

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

var word = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
var sha = lazyre.New(`^[a-f0-9]{40}$`)

type Mirror struct {
	Machine  string `json:"machine"`
	Target   string `json:"target"`
	Port     int    `json:"port,omitempty"`
	Identity string `json:"identity,omitempty"`
}
type Definition struct {
	ID           string            `json:"id"`
	Capacity     int               `json:"capacity"`
	Mirror       Mirror            `json:"mirror"`
	Repositories map[string]string `json:"repositories,omitempty"`
}
type Job struct {
	Ticket      string    `json:"ticket"`
	Slot        int       `json:"slot,omitempty"`
	Position    int       `json:"position,omitempty"`
	Project     string    `json:"project"`
	Ref         string    `json:"ref"`
	Commit      string    `json:"commit"`
	Groups      []string  `json:"groups"`
	Tag         string    `json:"tag"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	EnqueuedAt  time.Time `json:"enqueued_at,omitempty"`
	Run         string    `json:"run,omitempty"`
	DuplicateOf string    `json:"duplicate_of,omitempty"`
}
type Queue struct {
	V       int       `json:"v"`
	Slots   int       `json:"slots"`
	Now     time.Time `json:"now"`
	Running []Job     `json:"running"`
	Waiting []Job     `json:"waiting"`
}
type Outcome struct {
	V          int       `json:"v,omitempty"`
	Exit       int       `json:"exit"`
	Run        string    `json:"run"`
	FinishedAt time.Time `json:"finished_at"`
	Output     string    `json:"output,omitempty"`
}
type Snapshot struct {
	Queue       *Queue    `json:"queue,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
	AttemptedAt time.Time `json:"attempted_at"`
	Unknown     bool      `json:"unknown"`
	Raw         string    `json:"raw,omitempty"`
}
type Holder struct {
	Machine string `json:"machine"`
	Session string `json:"session"`
	Label   string `json:"label,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Todo    string `json:"todo,omitempty"`
}

func (h Holder) Owner() string { return h.Machine + "/" + h.Session }

type Lease struct {
	External       bool      `json:"external,omitempty"`
	ID             string    `json:"id"`
	Resource       string    `json:"resource"`
	State          string    `json:"state"`
	Key            string    `json:"dedupe_key"`
	Holder         Holder    `json:"holder"`
	Joiners        []string  `json:"joiners,omitempty"`
	Token          int64     `json:"token"`
	Seq            int64     `json:"seq"`
	Priority       int       `json:"priority"`
	CreatedAt      time.Time `json:"created_at"`
	GrantedAt      time.Time `json:"granted_at,omitempty"`
	HeartbeatUntil time.Time `json:"heartbeat_until,omitempty"`
	HoldUntil      time.Time `json:"hold_until,omitempty"`
	WaitUntil      time.Time `json:"wait_until"`
	Outcome        *Outcome  `json:"outcome,omitempty"`
	ExternalRef    string    `json:"external_ref,omitempty"`
}
type Request struct {
	Priority int      `json:"priority,omitempty"`
	Action   string   `json:"action"`
	Resource string   `json:"resource"`
	ID       string   `json:"lease_id,omitempty"`
	Token    int64    `json:"token"`
	Project  string   `json:"project,omitempty"`
	Commit   string   `json:"commit,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	Holder   Holder   `json:"holder"`
	Outcome  *Outcome `json:"outcome,omitempty"`
	Cached   *Outcome `json:"cached,omitempty"`
}
type Reply struct {
	Pending  []string `json:"pending,omitempty"`
	Status   string   `json:"status"`
	Lease    *Lease   `json:"lease,omitempty"`
	Position int      `json:"position,omitempty"`
}
type Status struct {
	Definition
	MirrorState Snapshot `json:"mirror_state"`
	Leases      []Lease  `json:"leases"`
}

func NormalizeGroups(groups []string) ([]string, error) {
	if len(groups) == 0 {
		groups = []string{"all"}
	}
	groups = slices.Clone(groups)
	for _, g := range groups {
		if !word.MatchString(g) || strings.Contains(g, "/") {
			return nil, errors.New("invalid gate group")
		}
	}
	slices.Sort(groups)
	return slices.Compact(groups), nil
}
func Key(project, commit string, groups []string) (string, error) {
	if !word.MatchString(project) || strings.Contains(project, "/") || !sha.MatchString(commit) {
		return "", errors.New("project and full commit are required")
	}
	groups, err := NormalizeGroups(groups)
	if err != nil {
		return "", err
	}
	return project + "|" + commit + "|" + strings.Join(groups, ","), nil
}
func ValidRef(ref string) bool { return word.MatchString(ref) && !strings.Contains(ref, "..") }
func ParseQueue(raw []byte) (*Queue, error) {
	var q Queue
	if err := json.Unmarshal(raw, &q); err != nil {
		return nil, err
	}
	if q.V != 1 || q.Slots < 1 || q.Now.IsZero() || q.Running == nil || q.Waiting == nil {
		return nil, errors.New("unknown queue schema")
	}
	seen := map[string]string{}
	for _, list := range [][]Job{q.Running, q.Waiting} {
		for i := range list {
			j := &list[i]
			if key, err := Key(j.Project, j.Commit, j.Groups); err == nil {
				if first := seen[key]; first != "" {
					j.DuplicateOf = first
				} else {
					seen[key] = j.Ticket
					if seen[key] == "" {
						seen[key] = j.Identity()
					}
				}
			}
		}
	}
	return &q, nil
}
func ParseResult(raw []byte) (*Outcome, error) {
	if strings.TrimSpace(string(raw)) == "null" || len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var fields struct {
		V          int    `json:"v"`
		Exit       *int   `json:"exit"`
		Run        string `json:"run"`
		FinishedAt string `json:"finished_at"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields.V != 1 {
		return nil, errors.New("unknown result schema")
	}
	if fields.Exit == nil && fields.Run == "" && fields.FinishedAt == "" {
		return nil, nil
	}
	finished, err := time.Parse(time.RFC3339Nano, fields.FinishedAt)
	if err != nil || fields.Exit == nil || fields.Run == "" {
		return nil, errors.New("unknown result schema")
	}
	return &Outcome{V: 1, Exit: *fields.Exit, Run: fields.Run, FinishedAt: finished}, nil
}

// Identity retains legacy entries without inventing a runner ticket or a dedupe commit.
func (j Job) Identity() string {
	if j.Run != "" {
		return j.Run
	}
	if j.Ticket != "" {
		return j.Ticket
	}
	if j.Slot > 0 {
		return "slot-" + strconv.Itoa(j.Slot) + "-" + j.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	return "waiting-" + strconv.Itoa(j.Position) + "-" + j.EnqueuedAt.UTC().Format(time.RFC3339Nano)
}
