package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// The hub mesh (docs/server/mesh.md): hubs paired with each other keep a link each (peer_link.go) and flood records of
// themselves (mesh_view.go). One hub of the group is its center (mesh_group.go): it serves every app and drives every
// computer, directly or through the other hubs, the members, which pass the apps' requests on to it (peer_http.go) and
// open streams to their computers' nodes for it (peer_node.go).

const (
	meshPublishDelay = 150 * time.Millisecond // records changing together go out as one
	meshRecordKeep   = 7 * 24 * time.Hour     // a record not heard of for this long is forgotten
	meshTTL          = 8                      // hops a forwarded message or request may take
)

// meshRefreshEvery: the record is rebuilt (round trips, availability), the computers' round trips measured and stale
// state pruned this often (a var: tests shorten it).
var meshRefreshEvery = 30 * time.Second

type meshState struct {
	mu           sync.Mutex
	running      bool // initMesh ran (a hub in unit tests has no mesh: everything is local)
	ctx          context.Context
	started      time.Time
	epoch        int64
	rev          int64
	links        map[string]*peerLink   // hub id -> its live link
	records      map[string]*hubRecord  // hub id -> its newest record, this hub's own included
	seenAt       map[string]time.Time   // hub id -> when its record last arrived or it was reachable
	peers        map[string]db.Peer     // the paired hubs (peers table)
	dialers      map[string]*peerDialer // hub id -> the loop dialing it
	built        []byte                 // the last record this hub published, without its rev
	publish      bool                   // a publish is scheduled
	saveAt       time.Time              // records last persisted
	dirty        bool                   // records changed since
	saveDeferred bool                   // persist deferred by snapshot pause

	group meshGroup // the center and its term (mesh_group.go)
	// handedTo: the center (and term) this hub last handed its device credentials to (mesh_group.go).
	handedTo string

	view atomic.Pointer[meshView]
	// changed is closed and replaced whenever the view changes (waiters: dialers, the admin page).
	changed chan struct{}
	// kick wakes viewLoop, which acts on the newest picture (applied: the last one it acted on), one at a time and
	// never in a link's read loop.
	kick    chan struct{}
	applied *meshView

	discovered map[string]discoveredHub // mDNS (mesh_discovery.go)
	dialErr    map[string]peerDialState // hub id -> how dialing it went

	server *peerServer
	ping   [2]time.Duration // tests: heartbeat interval and timeout

	// centerAPI is the API as the center serves it (http.go): the requests members pass on go there.
	centerAPI atomic.Value
	// proxied: requests this member is passing on to the center now (peer_http.go trackProxied).
	proxied map[*context.CancelFunc]struct{}
}

// initMesh loads what the mesh keeps (the group, peers, records) before the machines start connecting, so a member
// does not grab its computers in the first moments after a restart.
func (h *Hub) initMesh(ctx context.Context) {
	ms := &h.mesh
	ms.mu.Lock()
	ms.running, ms.ctx, ms.started, ms.epoch = true, ctx, time.Now(), time.Now().UnixNano()
	ms.links, ms.records, ms.seenAt = map[string]*peerLink{}, map[string]*hubRecord{}, map[string]time.Time{}
	ms.peers, ms.dialers = map[string]db.Peer{}, map[string]*peerDialer{}
	ms.changed, ms.kick = make(chan struct{}), make(chan struct{}, 1)
	ms.mu.Unlock()
	go h.viewLoop(ctx)
	if h.store != nil {
		// The epoch orders this run's records after the last run's, whatever the clock did meanwhile.
		var last int64
		if _, err := h.store.Setting(ctx, "mesh.epoch", &last); err != nil {
			h.logf("mesh: load epoch: %v", err)
		}
		ms.mu.Lock()
		ms.epoch = max(ms.epoch, last+1)
		epoch := ms.epoch
		ms.mu.Unlock()
		if err := h.store.PutSetting(ctx, "mesh.epoch", epoch); err != nil {
			h.logf("mesh: save epoch: %v", err)
		}
		var kept map[string]keptRecord
		if _, err := h.store.Setting(ctx, "mesh.records", &kept); err != nil {
			h.logf("mesh: load records: %v", err)
		}
		ms.mu.Lock()
		for id, k := range kept {
			if k.Record != nil && time.Since(k.Seen) < meshRecordKeep {
				ms.records[id], ms.seenAt[id] = k.Record, k.Seen
			}
		}
		ms.mu.Unlock()
	}
	h.loadGroup(ctx)
	if ps := h.peerStore(); ps != nil {
		list, err := ps.Peers(ctx)
		if err != nil {
			h.logf("mesh: load peers: %v", err)
		}
		ms.mu.Lock()
		for _, p := range list {
			ms.peers[p.HubID] = p
		}
		ms.mu.Unlock()
	}
	h.meshRebuild()
}

// startMesh starts what needs the hub's TLS identity and addresses: the dialers, discovery and the record loop.
func (h *Hub) startMesh(ctx context.Context) {
	h.startPeerServer(ctx)
	ms := &h.mesh
	ms.mu.Lock()
	var ids []string
	for id := range ms.peers {
		ids = append(ids, id)
	}
	ms.mu.Unlock()
	for _, id := range ids {
		h.startDialer(id)
	}
	go h.meshLoop(ctx)
	if h.cfg.Mesh.discovery() {
		go h.runDiscovery(ctx)
	}
	h.meshTouch()
}

type keptRecord struct {
	Record *hubRecord `json:"record"`
	Seen   time.Time  `json:"seen"`
}

// meshView is the current picture; nil while the mesh is not running.
func (h *Hub) meshView() *meshView { return h.mesh.view.Load() }

// meshTouch schedules this hub's record to be built and, when it changed, sent.
func (h *Hub) meshTouch() {
	ms := &h.mesh
	ms.mu.Lock()
	if !ms.running || ms.publish {
		ms.mu.Unlock()
		return
	}
	ms.publish = true
	ms.mu.Unlock()
	time.AfterFunc(meshPublishDelay, func() {
		ms.mu.Lock()
		ms.publish = false
		ms.mu.Unlock()
		h.meshPublish(false)
	})
}

// meshPublish builds this hub's record and floods it when it differs from the last one (or always, when forced).
func (h *Hub) meshPublish(force bool) {
	rec := h.buildRecord()
	body, _ := json.Marshal(rec)
	ms := &h.mesh
	ms.mu.Lock()
	if !ms.running || !force && bytes.Equal(body, ms.built) {
		ms.mu.Unlock()
		return
	}
	ms.built = body
	ms.rev++
	rec.Epoch, rec.Rev = ms.epoch, ms.rev
	ms.records[rec.HubID], ms.seenAt[rec.HubID] = rec, time.Now()
	links := ms.linkList()
	ms.mu.Unlock()
	for _, l := range links {
		l.send("rec", rec)
	}
	h.meshRebuild()
}

// buildRecord is this hub's record as things are now (epoch and rev are set when it is published).
func (h *Hub) buildRecord() *hubRecord {
	port, fp := h.tlsPin()
	rec := &hubRecord{HubID: h.hubID(), Name: h.hubName(), Version: h.version, TLSPort: port, FP: fp, PlainEndpoints: h.plainEndpoints()}
	for _, e := range h.endpointList() {
		if e.Scheme == "https" {
			rec.Endpoints = append(rec.Endpoints, e.URL)
		}
	}
	ms := &h.mesh
	ms.mu.Lock()
	for _, l := range ms.linkList() {
		rec.Neighbors = append(rec.Neighbors, recNeighbor{HubID: l.hubID, RTTMS: l.rttMS()})
	}
	rec.Center, rec.Term = ms.group.Center, ms.group.Term
	ms.mu.Unlock()
	sort.Slice(rec.Neighbors, func(i, j int) bool { return rec.Neighbors[i].HubID < rec.Neighbors[j].HubID })
	rec.Machines = h.recordEntries() // how the manager reaches each (devices_link.go)
	return rec
}

// linkList is the live links (ms.mu held).
func (ms *meshState) linkList() []*peerLink {
	out := make([]*peerLink, 0, len(ms.links))
	for _, l := range ms.links {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].hubID < out[j].hubID })
	return out
}

// meshRecord takes a record a link brought: a newer one is kept, passed on to the other links, and the picture
// worked out again.
func (h *Hub) meshRecord(from *peerLink, rec *hubRecord) {
	if rec == nil || rec.HubID == "" || rec.HubID == h.hubID() {
		return
	}
	ms := &h.mesh
	ms.mu.Lock()
	if cur := ms.records[rec.HubID]; !rec.newer(cur) {
		ms.mu.Unlock()
		return
	}
	ms.records[rec.HubID], ms.seenAt[rec.HubID], ms.dirty = rec, time.Now(), true
	var others []*peerLink
	for _, l := range ms.linkList() {
		if l != from {
			others = append(others, l)
		}
	}
	_, direct := ms.peers[rec.HubID]
	ms.mu.Unlock()
	for _, l := range others {
		l.send("rec", rec)
	}
	if direct {
		if ps := h.peerStore(); ps != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = ps.UpdatePeerSeen(ctx, rec.HubID, rec.Name, rankEndpoints(slices.Concat(rec.Endpoints, rec.PlainEndpoints)))
			}()
		}
	}
	h.adoptGroup(rec.Center, rec.Term, "the record of "+shortID(rec.HubID)) // a larger term elsewhere wins (mesh_group.go)
	h.meshRebuild()
}

// meshRebuild works the picture out again from the records and acts on what changed.
func (h *Hub) meshRebuild() {
	ms := &h.mesh
	ms.mu.Lock()
	if !ms.running {
		ms.mu.Unlock()
		return
	}
	self := h.hubID()
	recs := make(map[string]*hubRecord, len(ms.records)+1)
	for id, r := range ms.records {
		if id == self || time.Since(ms.seenAt[id]) < meshRecordKeep {
			recs[id] = r
		}
	}
	if recs[self] == nil {
		recs[self] = &hubRecord{HubID: self, Name: h.hubName()}
	}
	// This hub's own links are the truth for its neighbors, whatever its last published record says.
	own := *recs[self]
	own.Neighbors = nil
	for _, l := range ms.linkList() {
		own.Neighbors = append(own.Neighbors, recNeighbor{HubID: l.hubID, RTTMS: l.rttMS()})
	}
	recs[self] = &own
	v := computeView(self, recs)
	ms.view.Store(v)
	for id, vh := range v.hubs {
		if vh.reachable {
			ms.seenAt[id] = time.Now()
		}
	}
	close(ms.changed)
	ms.changed = make(chan struct{})
	ms.mu.Unlock()
	select {
	case ms.kick <- struct{}{}:
	default:
	}
}

// viewLoop acts on the newest picture whenever it changes (meshViewChanged), one change at a time, and repeats the
// revoked credentials now and then.
func (h *Hub) viewLoop(ctx context.Context) {
	ms := &h.mesh
	resend := time.NewTicker(revocationResendEvery)
	defer resend.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-resend.C:
			h.sendRevocations(newlyReachable(nil, ms.view.Load()))
			continue
		case <-ms.kick:
		}
		v := ms.view.Load()
		old := ms.applied
		ms.applied = v
		h.meshViewChanged(old, v)
	}
}

// meshViewChanged acts on a new picture: the paths to the computers the members offer, the link's group size, handing
// this hub's credentials to the center once it can be reached, and telling a hub that is back what was revoked.
func (h *Hub) meshViewChanged(old, v *meshView) {
	if reachableHubs(old) != reachableHubs(v) {
		h.sendLinkEndpoints() // persona-link shows how many hubs the group has
	}
	h.fleetRegroup() // the members' computers (machines.go)
	h.handOverCredentials()
	h.sendRevocations(newlyReachable(old, v))
}

// newlyReachable is the other hubs v can reach and old could not.
func newlyReachable(old, v *meshView) []string {
	var out []string
	for _, id := range v.reachableIDs() {
		if id != v.self && !old.reachable(id) {
			out = append(out, id)
		}
	}
	return out
}

// reachableHubs is how many hubs of the group are online, this one included.
func reachableHubs(v *meshView) int {
	if v == nil {
		return 1
	}
	n := 0
	for _, vh := range v.hubs {
		if vh.reachable {
			n++
		}
	}
	return max(n, 1)
}

// meshLoop rebuilds the record now and then (round trips change; the manager measures those to the computers) and
// keeps the records on disk.
func (h *Hub) meshLoop(ctx context.Context) {
	t := time.NewTicker(meshRefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.meshPublish(false)
		h.meshPersist(false)
		h.fleetRegroup()
	}
}

// meshPersist keeps the records at most once a minute (the hubs and their computers are known before the links are
// back after a restart).
func (h *Hub) meshPersist(force bool) {
	if h.store == nil {
		return
	}
	ms := &h.mesh
	ms.mu.Lock()
	if !force && (!ms.dirty || time.Since(ms.saveAt) < time.Minute) {
		ms.mu.Unlock()
		return
	}
	kept := map[string]keptRecord{}
	self := h.hubID()
	for id, r := range ms.records {
		if id != self {
			kept[id] = keptRecord{Record: r, Seen: ms.seenAt[id]}
		}
	}
	ms.dirty, ms.saveAt = false, time.Now()
	ms.mu.Unlock()
	if !h.quiesce.beginOrDefer(func() {
		ms.mu.Lock()
		ms.dirty = true
		ms.saveDeferred = true
		ms.mu.Unlock()
	}) {
		return
	}
	defer h.quiesce.end()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.store.PutSetting(ctx, "mesh.records", kept); err != nil {
		h.logf("mesh: save records: %v", err)
	}
}

func (h *Hub) resumeMeshPersist() {
	if h.store == nil {
		return
	}
	ms := &h.mesh
	ms.mu.Lock()
	deferred := ms.saveDeferred
	ms.saveDeferred = false
	ms.mu.Unlock()
	if !deferred {
		return
	}
	h.nodeWorkers.Add(1)
	go func() {
		defer h.nodeWorkers.Done()
		h.meshPersist(true)
	}()
}

// meshHub is a hub of the group as the apps see it in hello's mesh (docs/archive/operations/plan-hub-mesh.md 2.6, plan-hub-center.md
// 5): this hub, every hub it can reach, and the paired ones it cannot reach now. Center marks the group's center.
type meshHub struct {
	HubID     string   `json:"hub_id"`
	Name      string   `json:"name"`
	Online    bool     `json:"online"`
	Self      bool     `json:"self,omitempty"`
	Center    bool     `json:"center"`
	Endpoints []string `json:"endpoints"`
	TLSPort   int      `json:"tls_port,omitempty"`
	FP        string   `json:"fp,omitempty"`
}

func (h *Hub) meshHubs() []meshHub {
	self := h.hubID()
	center := h.centerID()
	port, fp := h.tlsPin()
	me := meshHub{HubID: self, Name: h.hubName(), Online: true, Self: true, Center: center == self, Endpoints: []string{},
		TLSPort: port, FP: fp}
	for _, e := range h.endpointList() {
		if e.Scheme == "https" || port == 0 {
			me.Endpoints = append(me.Endpoints, e.URL)
		}
	}
	out := []meshHub{me}
	v := h.meshView()
	if v == nil {
		return out
	}
	ms := &h.mesh
	ms.mu.Lock()
	peers := map[string]db.Peer{}
	for id, p := range ms.peers {
		peers[id] = p
	}
	ms.mu.Unlock()
	var rest []meshHub
	for id, vh := range v.hubs {
		_, paired := peers[id]
		if id == self || !vh.reachable && !paired && id != center {
			continue
		}
		mh := meshHub{HubID: id, Name: vh.rec.Name, Online: vh.reachable, Center: id == center,
			Endpoints: append([]string{}, vh.rec.Endpoints...), TLSPort: vh.rec.TLSPort, FP: vh.rec.FP}
		if p, ok := peers[id]; ok {
			if mh.Name == "" {
				mh.Name = p.Name
			}
			if len(mh.Endpoints) == 0 {
				mh.Endpoints = httpsOnly(p.Endpoints)
			}
			if mh.FP == "" {
				mh.FP = p.FP
			}
		}
		rest = append(rest, mh)
	}
	for id, p := range peers {
		if v.hubs[id] == nil {
			rest = append(rest, meshHub{HubID: id, Name: p.Name, Center: id == center, Endpoints: httpsOnly(p.Endpoints), FP: p.FP})
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Center != rest[j].Center {
			return rest[i].Center
		}
		if rest[i].Name != rest[j].Name {
			return rest[i].Name < rest[j].Name
		}
		return rest[i].HubID < rest[j].HubID
	})
	return append(out, rest...)
}

// meshWait returns a channel closed at the next change of the picture.
func (h *Hub) meshWait() <-chan struct{} {
	ms := &h.mesh
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.changed == nil {
		ms.changed = make(chan struct{})
	}
	return ms.changed
}

// hubLabel is a hub for the log: its name and the start of its id.
func (h *Hub) hubLabel(id string) string {
	if n := h.hubNameOf(id); n != "" {
		return "\"" + n + "\" (" + shortID(id) + ")"
	}
	return shortID(id)
}

// hubNameOf is what a hub of the group is called ("" when unknown).
func (h *Hub) hubNameOf(id string) string {
	if id == h.hubID() {
		return h.hubName()
	}
	if v := h.meshView(); v != nil {
		if vh := v.hubs[id]; vh != nil && vh.rec != nil && vh.rec.Name != "" {
			return vh.rec.Name
		}
	}
	ms := &h.mesh
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if p, ok := ms.peers[id]; ok {
		return p.Name
	}
	if r := ms.records[id]; r != nil {
		return r.Name
	}
	return ""
}
