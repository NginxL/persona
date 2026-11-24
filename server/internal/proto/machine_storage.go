package proto

// Machine storage monitoring, on-demand bounded hotspot analysis, and confirmation-gated selective cleanup (#12349).
// Mount stats are read-only statfs reads (no background du). Hotspot scans and cleanups only operate on fixed,
// node-resolved allowlisted categories and target IDs — callers never supply arbitrary paths or shell commands.

// Node RPC methods for per-mount storage monitoring, hotspot analysis, and selective cleanup.
const (
	MethodMachineStorage         = "machine.storage"          // {} -> MachineStorageReport
	MethodMachineStorageHotspots = "machine.storage.hotspots" // StorageHotspotsParams -> MachineStorageHotspotsReport
	MethodMachineStorageCleanup  = "machine.storage.cleanup"  // StorageCleanupParams -> StorageCleanupResult
)

// Hotspot category IDs reported by machine.storage.hotspots.
const (
	StorageCategoryGitWorktrees   = "git-worktrees"
	StorageCategoryDocker         = "docker"
	StorageCategoryUserCache      = "user-cache"
	StorageCategoryGoBuild        = "go-build"
	StorageCategoryGoMod          = "go-mod"
	StorageCategoryNpmCache       = "npm-cache"
	StorageCategoryPipCache       = "pip-cache"
	StorageCategoryPersonaLogs    = "persona-logs"
	StorageCategoryPersonaBackups = "persona-backups"
)

// Allowlisted safe cleanup target IDs accepted by machine.storage.cleanup.
const (
	StorageTargetGoBuild           = "go-build"
	StorageTargetGoMod             = "go-mod"
	StorageTargetNpmCache          = "npm-cache"
	StorageTargetPipCache          = "pip-cache"
	StorageTargetPersonaLogs       = "persona-logs"
	StorageTargetPersonaBackups    = "persona-backups"
	StorageTargetDockerBuildCache  = "docker-build-cache"
	StorageTargetGitWorktreesPrune = "git-worktrees-prune"
)

// StorageCleanupTargetIDs is the closed allowlist of safe cleanup target IDs.
var StorageCleanupTargetIDs = []string{
	StorageTargetGoBuild,
	StorageTargetGoMod,
	StorageTargetNpmCache,
	StorageTargetPipCache,
	StorageTargetPersonaLogs,
	StorageTargetPersonaBackups,
	StorageTargetDockerBuildCache,
	StorageTargetGitWorktreesPrune,
}

// Hotspot category scan statuses.
const (
	StorageStatusOK          = "ok"
	StorageStatusPartial     = "partial"
	StorageStatusEmpty       = "empty"
	StorageStatusUnavailable = "unavailable"
)

// Selective cleanup item outcomes.
const (
	StorageCleanupPreview = "preview"
	StorageCleanupCleaned = "cleaned"
	StorageCleanupInUse   = "in_use"
	StorageCleanupMissing = "missing"
	StorageCleanupSkipped = "skipped"
	StorageCleanupFailed  = "failed"
)

// MountStat reports capacity and inode utilization for a single mounted filesystem.
type MountStat struct {
	Mount         string   `json:"mount"`
	Device        string   `json:"device,omitempty"`
	FSType        string   `json:"fs_type,omitempty"`
	UsedBytes     int64    `json:"used_bytes"`
	AvailBytes    int64    `json:"avail_bytes"`
	TotalBytes    int64    `json:"total_bytes"`
	UsedPercent   float64  `json:"used_percent"`
	InodesTotal   uint64   `json:"inodes_total,omitempty"`
	InodesUsed    uint64   `json:"inodes_used,omitempty"`
	InodesFree    uint64   `json:"inodes_free,omitempty"`
	InodesPercent *float64 `json:"inodes_percent,omitempty"` // nil when filesystem reports 0 inodes
	ReadOnly      bool     `json:"read_only,omitempty"`
}

// MachineStorageReport is the fast read-only answer of machine.storage (statfs only).
type MachineStorageReport struct {
	MachineID   string      `json:"machine_id,omitempty"`
	At          int64       `json:"at"`
	OS          string      `json:"os"`
	Supported   bool        `json:"supported"`
	Mounts      []MountStat `json:"mounts"`
	CollectedMS int64       `json:"collected_ms"`
}

// StorageHotspotsParams configures bounded limits for machine.storage.hotspots.
type StorageHotspotsParams struct {
	TimeoutMS int `json:"timeout_ms,omitempty"`
	MaxDepth  int `json:"max_depth,omitempty"`
}

// StorageHotspotItem describes one notable entry inside a hotspot category (e.g., a worktree or Docker storage type).
type StorageHotspotItem struct {
	Label            string `json:"label"`
	Path             string `json:"path,omitempty"`
	Bytes            int64  `json:"bytes"`
	ReclaimableBytes int64  `json:"reclaimable_bytes,omitempty"`
	Stale            bool   `json:"stale,omitempty"`
	Detail           string `json:"detail,omitempty"`
}

// StorageHotspotCategory summarizes one high-signal storage category from an on-demand hotspot scan.
type StorageHotspotCategory struct {
	ID               string               `json:"id"`
	Path             string               `json:"path,omitempty"`
	Bytes            int64                `json:"bytes"`
	ReclaimableBytes int64                `json:"reclaimable_bytes,omitempty"`
	Status           string               `json:"status"` // StorageStatus*
	Cleanable        bool                 `json:"cleanable"`
	CleanupTargetID  string               `json:"cleanup_target_id,omitempty"`
	InUse            bool                 `json:"in_use,omitempty"`
	ItemCount        int                  `json:"item_count,omitempty"`
	Items            []StorageHotspotItem `json:"items,omitempty"`
	Note             string               `json:"note,omitempty"`
}

// MachineStorageHotspotsReport is the bounded on-demand hotspot analysis answer.
type MachineStorageHotspotsReport struct {
	MachineID  string                   `json:"machine_id,omitempty"`
	At         int64                    `json:"at"`
	OS         string                   `json:"os"`
	Partial    bool                     `json:"partial"`
	DurationMS int64                    `json:"duration_ms"`
	Categories []StorageHotspotCategory `json:"categories"`
}

// StorageCleanupParams is the request to preview (dry_run=true) or execute (confirm=true) selective safe cleanup.
type StorageCleanupParams struct {
	Targets []string `json:"targets"`
	DryRun  bool     `json:"dry_run,omitempty"`
	Confirm bool     `json:"confirm,omitempty"`
}

// StorageCleanupItem reports one allowlisted cleanup target's preview or execution outcome.
type StorageCleanupItem struct {
	TargetID string `json:"target_id"`
	Path     string `json:"path,omitempty"`
	Action   string `json:"action,omitempty"`
	Bytes    int64  `json:"bytes"`
	Status   string `json:"status"` // StorageCleanup*
	Error    string `json:"error,omitempty"`
}

// StorageCleanupResult is returned by machine.storage.cleanup for both preview and confirmed runs.
type StorageCleanupResult struct {
	MachineID       string               `json:"machine_id,omitempty"`
	DryRun          bool                 `json:"dry_run"`
	EstimatedBytes  int64                `json:"estimated_bytes"`
	FreedBytes      int64                `json:"freed_bytes"`
	AvailableBefore int64                `json:"available_before,omitempty"`
	AvailableAfter  int64                `json:"available_after,omitempty"`
	Items           []StorageCleanupItem `json:"items"`
}
