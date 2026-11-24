package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/proto"
	rq "github.com/nwverse/persona/server/internal/resourcequeue"
)

var resourceToolNames = []string{"resource_status", "resource_acquire", "resource_get", "resource_renew", "resource_release", "resource_cancel"}

const resourceDefinitionsKey = "shared_resources"

type resourceDB interface {
	Transaction(context.Context, func(pgx.Tx) error) error
}

func (s *Service) resourceDefinitions(ctx context.Context) ([]rq.Definition, error) {
	out := []rq.Definition{}
	if st := s.store(); st != nil {
		_, err := st.Setting(ctx, resourceDefinitionsKey, &out)
		return out, err
	}
	return out, nil
}
func (s *Service) resourceDefinition(ctx context.Context, id string) (rq.Definition, error) {
	defs, err := s.resourceDefinitions(ctx)
	if err != nil {
		return rq.Definition{}, err
	}
	for _, d := range defs {
		if d.ID == id {
			return d, nil
		}
	}
	return rq.Definition{}, errors.New("resource not registered")
}
func resourceMirrorKey(id string) string { return "shared_resources.mirror." + id }
func (s *Service) resourceMirror(ctx context.Context, id string) rq.Snapshot {
	var snap rq.Snapshot
	snap.Unknown = true
	if st := s.store(); st != nil {
		_, _ = st.Setting(ctx, resourceMirrorKey(id), &snap)
	}
	return snap
}

// resourceRows serializes all admission and settlement on the resource, including across manager restarts.
func (s *Service) resourceRows(ctx context.Context, d rq.Definition, fn func(*[]rq.Lease, int64) (rq.Reply, error)) (rq.Reply, []rq.Lease, error) {
	st, ok := s.store().(resourceDB)
	if !ok {
		return rq.Reply{}, nil, errors.New("resource database unavailable")
	}
	var reply rq.Reply
	var leases []rq.Lease
	originalBodies := map[string]string{}
	snap := s.resourceMirror(ctx, d.ID)
	err := st.Transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "resource:"+d.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT body FROM resource_leases WHERE resource=$1 AND (state IN ('queued','granted') OR updated_at > now()-interval '12 hours') ORDER BY seq`, d.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			var l rq.Lease
			if err := json.Unmarshal(raw, &l); err != nil {
				rows.Close()
				return err
			}
			leases = append(leases, l)
			canonical, e := json.Marshal(l)
			if e != nil {
				rows.Close()
				return e
			}
			originalBodies[l.ID] = string(canonical)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		heldBefore := map[string]bool{}
		for _, l := range leases {
			heldBefore[l.ID] = l.State == "granted"
		}
		var seq int64
		if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('resource_leases','seq'))`).Scan(&seq); err != nil {
			return err
		}
		now := time.Now().UTC()
		rq.Expire(leases, now)
		freshMirror := !snap.Unknown && snap.Queue != nil && now.Sub(snap.ObservedAt) < 30*time.Second
		// External tickets also participate in dedupe, including submissions made by older clients.
		if freshMirror {
			for _, list := range [][]rq.Job{snap.Queue.Running, snap.Queue.Waiting} {
				for _, job := range list {
					key, e := rq.Key(job.Project, job.Commit, job.Groups)
					if e != nil {
						continue
					}
					found := false
					for i := range leases {
						l := &leases[i]
						if l.Key == key && (l.State == "queued" || l.State == "granted") {
							found = true
							if !l.External && job.Tag == l.ID && job.Run != "" {
								l.ExternalRef = job.Run
							}
							if l.External {
								if job.Slot > 0 {
									l.State = "granted"
								} else {
									l.State = "queued"
								}
								l.HeartbeatUntil = snap.ObservedAt.Add(90 * time.Second)
								l.HoldUntil = snap.ObservedAt.Add(time.Hour)
							}
							break
						}
					}
					if found {
						continue
					}
					var externalSeq int64
					if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('resource_leases','seq'))`).Scan(&externalSeq); err != nil {
						return err
					}
					state := "queued"
					if job.Slot > 0 {
						state = "granted"
					}
					leases = append(leases, rq.Lease{ID: randomHex(16), Seq: externalSeq, Resource: d.ID, Key: key, State: state, External: true, ExternalRef: job.Identity(), Holder: rq.Holder{Machine: d.Mirror.Machine, Session: "runner:" + job.Identity(), Label: job.Project}, CreatedAt: now, WaitUntil: now.Add(90 * time.Minute), HeartbeatUntil: now.Add(90 * time.Second), HoldUntil: now.Add(time.Hour)})
				}
			}
		}
		external := 0
		if freshMirror {
			for _, j := range snap.Queue.Running {
				found := false
				for _, l := range leases {
					if l.State == "granted" && (j.Tag == l.ID || l.External && l.ExternalRef == j.Identity()) {
						found = true
						break
					}
				}
				if !found {
					external++
				}
			}
		}
		rq.Advance(leases, d.Capacity, external, now)
		if fn != nil {
			reply, err = fn(&leases, seq)
			if err != nil {
				return err
			}
		}
		rq.Advance(leases, d.Capacity, external, now)
		if reply.Lease != nil {
			for i := range leases {
				if leases[i].ID == reply.Lease.ID {
					reply.Lease = &leases[i]
					break
				}
			}
			if reply.Status == "queued" {
				reply.Status = reply.Lease.State
			}
		}
		if reply.Lease != nil && reply.Lease.State == "queued" {
			reply.Position = 1
			for _, l := range leases {
				if l.State == "queued" && l.ID != reply.Lease.ID && (l.Priority > reply.Lease.Priority || (l.Priority == reply.Lease.Priority && l.Seq < reply.Lease.Seq)) {
					reply.Position++
				}
			}
		}
		// Request order and grant order differ under priority. A durable sequence fences grants in grant order,
		// including after the recent-result window was pruned from this transaction's read.
		for i := range leases {
			l := &leases[i]
			if l.State == "granted" && !heldBefore[l.ID] {
				if err := tx.QueryRow(ctx, `SELECT nextval('resource_fence_tokens')`).Scan(&l.Token); err != nil {
					return err
				}
			}
		}
		// Status polling does not rewrite unchanged leases or their retained result logs.
		updates := map[string][]byte{}
		for _, l := range leases {
			body, e := json.Marshal(l)
			if e != nil {
				return e
			}
			if originalBodies[l.ID] != string(body) {
				updates[l.ID] = body
			}
		}
		// Expire old active rows before inserting replacements, preserving the partial unique index.
		for _, l := range leases {
			if updates[l.ID] != nil && originalBodies[l.ID] != "" && l.State != "queued" && l.State != "granted" {
				if _, err := tx.Exec(ctx, `UPDATE resource_leases SET state=$2 WHERE id=$1`, l.ID, l.State); err != nil {
					return err
				}
			}
		}
		for _, l := range leases {
			body, changed := updates[l.ID]
			if !changed {
				continue
			}
			if tag, e := tx.Exec(ctx, `INSERT INTO resource_leases(id,resource,seq,state,dedupe_key,body) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET state=EXCLUDED.state,body=EXCLUDED.body,updated_at=CASE WHEN resource_leases.body IS DISTINCT FROM EXCLUDED.body THEN now() ELSE resource_leases.updated_at END WHERE resource_leases.resource=EXCLUDED.resource AND resource_leases.dedupe_key=EXCLUDED.dedupe_key`, l.ID, d.ID, l.Seq, l.State, l.Key, body); e != nil {
				return e
			} else if tag.RowsAffected() != 1 {
				return errors.New("request ID already belongs to another key")
			}
		}
		return nil
	})
	if leases == nil {
		leases = []rq.Lease{}
	}
	return reply, leases, err
}
func (s *Service) resourceRequest(ctx context.Context, in rq.Request) (rq.Reply, error) {
	d, err := s.resourceDefinition(ctx, in.Resource)
	if err != nil {
		return rq.Reply{}, err
	}
	if in.Priority < 0 || in.Priority > 1 {
		return rq.Reply{}, errors.New("invalid priority")
	}
	if in.Outcome != nil && len(in.Outcome.Output) > 256<<10 {
		return rq.Reply{}, errors.New("result output exceeds 256 KiB")
	}
	in.Cached = nil
	if in.Action == "acquire" && in.ID != "" {
		in.ID = fmt.Sprintf("%x", sha256.Sum256([]byte(in.Resource+"|"+in.Holder.Owner()+"|"+in.ID)))
	}
	if in.Action == "acquire" {
		if _, err := rq.Key(in.Project, in.Commit, in.Groups); err != nil {
			return rq.Reply{}, err
		}
		// Only the trusted node transport can attest an external successful result.
		check, cancel := context.WithTimeout(ctx, 15*time.Second)
		var o *rq.Outcome
		snapshot := s.resourceMirror(ctx, d.ID)
		if s.devs != nil && snapshot.Queue != nil && !snapshot.Unknown {
			raw, e := s.devs.CallNode(check, d.Mirror.Machine, "resource.result", resourceParams(map[string]any{"mirror": d.Mirror, "project": in.Project, "commit": in.Commit, "groups": in.Groups}))
			if e == nil {
				_ = json.Unmarshal(raw, &o)
			}
		}
		cancel()
		in.Cached = o
	}
	rep, _, err := s.resourceRows(ctx, d, func(rows *[]rq.Lease, seq int64) (rq.Reply, error) {
		if in.Action == "acquire" {
			return rq.Acquire(rows, in, time.Now().UTC(), randomHex(16), seq)
		}
		return rq.Mutate(*rows, in, time.Now().UTC())
	})
	return rep, err
}
func (s *Service) resourceStatus(ctx context.Context, id string) ([]rq.Status, error) {
	defs, err := s.resourceDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := []rq.Status{}
	for _, d := range defs {
		if id != "" && id != d.ID {
			continue
		}
		_, rows, err := s.resourceRows(ctx, d, nil)
		if err != nil {
			return nil, err
		}
		active := make([]rq.Lease, 0)
		for _, l := range rows {
			if l.State == "queued" || l.State == "granted" {
				active = append(active, l)
			}
		}
		out = append(out, rq.Status{Definition: d, MirrorState: s.resourceMirror(ctx, d.ID), Leases: active})
	}
	return out, nil
}
func (s *Service) resourceRoutes() {
	s.handle("POST /api/v1/resources/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, errors.New("full role required"))
			return
		}
		var in rq.Request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		in.Action = r.PathValue("action")
		switch in.Action {
		case "acquire", "get", "renew", "release", "cancel":
		default:
			apierr.Write(w, 400, errors.New("unknown resource action"))
			return
		}
		out, err := s.resourceRequest(r.Context(), in)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("GET /api/v1/resources", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.resourceStatus(r.Context(), r.URL.Query().Get("resource"))
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"resources": out})
	})
	s.handle("PUT /api/v1/resources", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("full role required"))
			return
		}
		var in struct {
			Resources []rq.Definition `json:"resources"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		seen := map[string]bool{}
		for _, d := range in.Resources {
			if !rq.ValidRef(d.ID) || d.Capacity < 1 || d.Capacity > 64 || d.Mirror.Machine == "" || d.Mirror.Target == "" || seen[d.ID] {
				apierr.Write(w, 400, errors.New("invalid resource definition"))
				return
			}
			seen[d.ID] = true
		}
		if s.store() == nil {
			apierr.Write(w, 503, errors.New("resource database unavailable"))
			return
		}
		if err := s.store().PutSetting(r.Context(), resourceDefinitionsKey, in.Resources); err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, in)
	})
}
func (s *Service) resourceTool(ctx context.Context, machine, session, name string, args json.RawMessage) proto.ToolResult {
	var in rq.Request
	if err := json.Unmarshal(args, &in); err != nil {
		return proto.TextResult(err.Error(), true)
	}
	var out any
	var err error
	if name == "resource_status" {
		var list []rq.Status
		list, err = s.resourceStatus(ctx, in.Resource)
		out = map[string]any{"resources": list}
	} else {
		in.Action = strings.TrimPrefix(name, "resource_")
		in.Priority = 0
		in.Holder.Machine = s.canonical(machine)
		in.Holder.Session = session
		out, err = s.resourceRequest(ctx, in)
	}
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	raw, _ := json.Marshal(out)
	return proto.TextResult(string(raw), false)
}
func (s *Service) resourceLoop(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		s.resourceRefresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (s *Service) resourceRefresh(ctx context.Context) {
	defs, err := s.resourceDefinitions(ctx)
	if err != nil {
		return
	}
	for _, d := range defs {
		if ctx.Err() != nil {
			return
		}
		snap := s.resourceMirror(ctx, d.ID)
		snap.AttemptedAt = time.Now().UTC()
		snap.Unknown = true
		check, cancel := context.WithTimeout(ctx, 25*time.Second)
		if s.devs != nil {
			raw, err := s.devs.CallNode(check, d.Mirror.Machine, "resource.queue", resourceParams(map[string]any{"mirror": d.Mirror}))
			if err == nil {
				var next rq.Snapshot
				if json.Unmarshal(raw, &next) == nil {
					if next.Queue != nil {
						snap = next
					} else {
						snap.Raw = next.Raw
					}
				}
			}
		}
		cancel()
		if s.store() != nil {
			_ = s.store().PutSetting(ctx, resourceMirrorKey(d.ID), snap)
		}
		if s.store() != nil && s.devs != nil && !snap.Unknown && snap.Queue != nil {
			_, rows, e := s.resourceRows(ctx, d, nil)
			if e == nil {
				for _, l := range rows {
					if !l.External || (l.State != "queued" && l.State != "granted") {
						continue
					}
					present := false
					for _, list := range [][]rq.Job{snap.Queue.Running, snap.Queue.Waiting} {
						for _, j := range list {
							if j.Identity() == l.ExternalRef {
								present = true
							}
						}
					}
					if present {
						continue
					}
					parts := strings.Split(l.Key, "|")
					if len(parts) != 3 {
						continue
					}
					check, done := context.WithTimeout(ctx, 15*time.Second)
					raw, e := s.devs.CallNode(check, d.Mirror.Machine, "resource.result", resourceParams(map[string]any{"mirror": d.Mirror, "project": parts[0], "commit": parts[1], "groups": strings.Split(parts[2], ",")}))
					done()
					var outcome *rq.Outcome
					if e == nil {
						_ = json.Unmarshal(raw, &outcome)
					}
					// The latest completed result may still belong to a previous same-key run.
					// Wait for this run's result; a missing result is not completion or cancellation.
					if outcome == nil || outcome.Run != l.ExternalRef {
						continue
					}
					_, _, _ = s.resourceRows(ctx, d, func(current *[]rq.Lease, _ int64) (rq.Reply, error) {
						for i := range *current {
							r := &(*current)[i]
							if r.ID == l.ID {
								r.State = "released"
								r.Outcome = outcome
							}
						}
						return rq.Reply{}, nil
					})
				}
			}
		}
		if s.store() != nil {
			_ = s.store().PutSetting(ctx, resourceMirrorKey(d.ID), snap)
		}
	}
	if list, err := s.resourceStatus(ctx, ""); err == nil && len(list) > 0 {
		s.event(ctx, map[string]any{"type": "resources", "resources": list})
	}
}

func resourceParams(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
