package nodeiphone

// Operating the iPhones this computer reaches over RemotePairing, for the assistant (the built-in extension iphone,
// docs/extensions/iphone.md). The built-in program iphone answers the manager: status (which iPhones there are and
// whether each can be operated now), call (one tool step on one of them) and pairing from the app. Two things it only
// hands on to: the device bridge's sessions with the paired iPhones (iphoneHost) and the core that turns a look or a
// do into the bridge's operations (iphoneDriver). Here are what the manager may ask, how long it waits, which iPhone
// a call is for, the screenshot as an image, and whether a failed step may have reached the iPhone. The manager asks
// only while the user has the extension on.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/modules/phone-extra/iphone"
	"github.com/nwverse/persona/server/internal/node/nodekit"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// iphoneHost is this computer's side of its iPhones: the device bridge, the pairing records and a session with each
// paired iPhone. Its errors carry the bridge's or the session's code (*rpc.Error, or an error with ErrorCode).
type iphoneHost interface {
	// Bridge: the device bridge is installed, and its version.
	Bridge(ctx context.Context) (installed bool, version string)
	// DDIPresent: this computer has the developer disk image Prepare mounts.
	DDIPresent() bool
	// Devices are the paired iPhones and those plugged in over USB.
	Devices(ctx context.Context) []iphoneLink
	// Call runs one of the bridge's operations (device_info, screenshot, tap…) on a paired iPhone through its session
	// and returns the operation's result. It answers an input only after the iPhone took it, and never replays one
	// whose answer was lost.
	Call(ctx context.Context, device, op string, args json.RawMessage) (json.RawMessage, error)
	// Connect starts a paired iPhone's session without sending anything; it stays up while idle.
	Connect(device string) error
	// Pair, Forget and Install (the bridge itself, from a release) start a background job (one at a time); Cancel
	// stops it; Job is the one running or the last.
	Pair(device string) error
	Forget(device string) error
	Install(src iphone.InstallSource) error
	Prepare(device string) error
	Cancel()
	Job() *iphoneJob
}

// iphoneLink is one iPhone as the host knows it: from its pairing record, or plugged in over USB, and its session.
type iphoneLink struct {
	ID, Name, ProductType, IOS string
	Paired, USB                bool
	// State: idle | connecting | connected | backoff | failed; Transport: network | usb.
	State, Transport string
	// ErrorCode: the bridge's or the session's code for its last failure.
	ErrorCode string
	// Info: the session's latest device_info of it, kept from its own checks (empty when it has none).
	Info json.RawMessage
	// DeveloperMode: what the iPhone last said of it, nil when it never did.
	DeveloperMode *bool
	// AutoPrepare: its owner prepared it once, so this computer mounts its developer disk image again by itself.
	AutoPrepare bool
}

// iphoneJob is the host's pairing (or install) job; ErrorCode is the bridge's code for why it failed, shown as its
// note. Waiting: while a pairing runs, what it waits for from the person at the phone (trust_pending, locked). Stage:
// while an install runs, what it does (download, verify, install). Device: the iPhone it is for.
type iphoneJob struct {
	Kind, State, Step      string
	ErrorCode, Error       string
	Waiting, Stage, Device string
	Started                time.Time
}

// iphoneDriver is the core that runs a look or a do (reading the screen, numbering its elements, acting on them,
// asking for approval) with the bridge's operations on one iPhone.
type iphoneDriver interface {
	Run(ctx context.Context, r iphoneRun) (iphoneOutcome, error)
}

// iphoneRun is one look or do for the driver.
type iphoneRun struct {
	Device, Session, Op string // Op: look | do; Device is an opaque id to the driver
	Args                json.RawMessage
	Approved            string // the approval id the driver gave earlier, as the user allowed it
	Confirm             bool   // the owner asks to approve every input, on top of the steps the rules always ask about
	// SelfApps and SelfNames are Persona's own apps on the iPhone (proto.IPhoneCall), which no step may operate.
	SelfApps, SelfNames []string
	// Bridge runs one of the bridge's operations on this iPhone.
	Bridge func(ctx context.Context, op string, args json.RawMessage) (json.RawMessage, error)
	// TempDir is where a screenshot is written (the bridge's out_file); it is removed after the call.
	TempDir string
}

// iphoneOutcome is what the driver answered: its result, a screenshot it had written into TempDir, or that the step
// needs approval first: Approval is what for, as data the manager words for the owner (proto.IPhoneResult.Approval).
type iphoneOutcome struct {
	Output        json.RawMessage
	Text          string
	ImageFile     string
	NeedsApproval bool
	Approval      *proto.IPhoneApproval
	ApprovalID    string
}

// iphones and iphoneCore are this computer's host and driver: the host is set by iphone_host.go and the driver is
// iphonectl's core (iphone_core.go); tests set their own.
var (
	iphones    iphoneHost   = noIPhones{}
	iphoneCore iphoneDriver = &iphoneControl{}
)

type noIPhones struct{}

func (noIPhones) Bridge(context.Context) (bool, string) { return false, "" }
func (noIPhones) DDIPresent() bool                      { return false }
func (noIPhones) Devices(context.Context) []iphoneLink  { return nil }
func (noIPhones) Connect(string) error                  { return errNoIPhoneBridge }
func (noIPhones) Pair(string) error                     { return errNoIPhoneBridge }
func (noIPhones) Forget(string) error                   { return errNoIPhoneBridge }
func (noIPhones) Install(iphone.InstallSource) error    { return errNoIPhoneBridge }
func (noIPhones) Prepare(string) error                  { return errNoIPhoneBridge }
func (noIPhones) Cancel()                               {}
func (noIPhones) Job() *iphoneJob                       { return nil }
func (noIPhones) Call(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
	return nil, errNoIPhoneBridge
}

var errNoIPhoneBridge = &rpc.Error{Code: "bridge_missing", Message: "the iPhone device bridge is not installed on this computer"}

// The notes the app shows for an iPhone, by the bridge's or the session's code. The hub translates them by their
// original (hub-iphone.json's rpc_*), so each must stay exactly as the catalog has it.
const (
	iphoneNoteNoBridge   = "这台电脑上没有装 iPhone 设备桥（persona-iphone-bridge）"
	iphoneNoteNonePaired = "还没有和这台电脑配对的 iPhone：用数据线把 iPhone 连到这台电脑，在机器页点「配对」，再在 iPhone 上点「信任」"
	iphoneNoteUSB        = "已用数据线连上，还没有配对：在机器页点「配对」"
	iphoneNoteConnecting = "正在连接这台 iPhone"
	iphoneNoteFailed     = "这台 iPhone 出了点问题，稍后再试"
	// A pairing job's step while it runs, and while it waits for the person at the phone (#8199).
	iphoneNotePairing    = "正在和这台 iPhone 配对"
	iphoneNotePairTrust  = "在这台 iPhone 上点「信任」，再输入锁屏密码"
	iphoneNotePairUnlock = "解锁这台 iPhone，配对才能继续"
)

// An install job's step while it runs, by its stage, and its error once failed, by its code (#8199).
var (
	iphoneInstallSteps = map[string]string{
		"download": "正在下载 iPhone 助手程序",
		"verify":   "正在校验 iPhone 助手程序",
		"install":  "正在安装 iPhone 助手程序",
	}
	iphonePrepareStep   = "正在准备这台 iPhone，大约一分钟"
	iphonePrepareErrors = map[string]string{
		"ddi_missing":            "这台电脑上没有 iPhone 需要的 Apple 开发者文件：先在这台 Mac 上从 App Store 装好 Xcode，再试一次",
		"ddi_unsupported_device": "这台电脑上的 Apple 开发者文件不支持这台 iPhone 的 iOS 版本",
		"tss_failed":             "没能从 Apple 拿到这台 iPhone 的签名：检查这台电脑的网络，再试一次",
		"mount_failed":           "这台 iPhone 没能准备好：解锁后再试",
		"timeout":                "准备这台 iPhone 超时了：解锁并保持亮屏，再试一次",
	}
	iphoneInstallErrors = map[string]string{
		"download_failed":   "下载 iPhone 助手程序失败：检查这台电脑的网络，再试一次",
		"checksum_mismatch": "下载的 iPhone 助手程序和发布的不一致：再试一次",
		"bad_helper":        "下载的 iPhone 助手程序在这台电脑上运行不了",
		"install_failed":    "这台电脑上装不上 iPhone 助手程序",
		"timeout":           "iPhone 助手程序没能及时装好：再试一次",
	}
)

var iphoneNotes = map[string]string{
	"unreachable":         "连不上这台 iPhone：让它开着机，并和这台电脑在同一个网络里",
	"pairing_missing":     "这台电脑上找不到它的配对记录：用数据线重新配对",
	"pairing_rejected":    "这台 iPhone 不再信任这台电脑（被移除或抹掉过）：用数据线重新配对",
	"wrong_device":        "这个地址上的不是这台 iPhone：等它重新被发现，或重新配对",
	"tunnel_failed":       "连上了这台 iPhone，但建立加密通道失败：稍后再试，或重新配对",
	"developer_mode_off":  "这台 iPhone 没打开开发者模式：在「设置 › 隐私与安全性 › 开发者模式」里打开，重启后确认",
	"ddi_not_mounted":     "这台 iPhone 还没装载开发者磁盘映像：用数据线把它连到这台电脑一次，装载后再试",
	"service_unavailable": "这台 iPhone 缺少需要的设备服务，操控不了",
	"locked":              "这台 iPhone 锁屏了：解锁后才能操控",
	"unsupported":         "这台 iPhone 的 iOS 版本不支持这项操作",
	"disconnected":        "和这台 iPhone 的连接断开了，正在重连",
	"timeout":             "这台 iPhone 没有及时回应",
	"trust_pending":       "在 iPhone 上点「信任」，再点一次「配对」",
	"not_found":           "数据线上找不到这台 iPhone：重新插上再试",
	"bridge_missing":      iphoneNoteNoBridge,
}

// iphoneCode is the code an app gets beside iphoneNote(code): the code itself, "failed" when there is none.
func iphoneCode(code string) string { return cmp.Or(code, "failed") }

// iphoneNote is the note for code; a code it does not know is a failure without a word on what to do.
func iphoneNote(code string) string {
	if n, ok := iphoneNotes[code]; ok {
		return n
	}
	return iphoneNoteFailed
}

// iphoneOps are what the manager may ask of an iPhone: its state, a look at its screen, and a step on it. The
// bridge's own operations (its files, apps' data, anything else) are only the driver's to use.
var iphoneOps = map[string]bool{"status": true, "look": true, "do": true}

// iphoneNotSentCodes are the codes that mean an operation was refused before it reached the iPhone: the node's own
// checks, the session's (nothing was written) and the bridge's refusals before acting.
var iphoneNotSentCodes = []string{
	"bad_op", "bad_args", "not_paired", "ambiguous_device", "bridge_missing",
	"bad_request", "pairing_missing", "pairing_rejected", "unreachable", "wrong_device", "tunnel_failed",
	"developer_mode_off", "ddi_not_mounted", "service_unavailable", "locked", "app_not_found", "unsupported",
}

// Bounds of one call's wait (IPhoneCall.TimeoutMS), the bridge's own; and of the status probe of one iPhone.
const (
	iphoneCallDefault = 30 * time.Second
	iphoneCallLeast   = time.Second
	iphoneCallMost    = 120 * time.Second
	iphoneProbe       = 5 * time.Second
)

// Status is what this computer has of iPhones now (proto.IPhone; Enabled and each Allowed are the manager's).
func Status(ctx context.Context) proto.IPhone {
	p := proto.IPhone{Devices: []proto.IPhoneDevice{}}
	p.Installed, p.Version = iphones.Bridge(ctx)
	present := iphones.DDIPresent()
	p.DDIPresent = &present
	if !p.Installed {
		p.Note, p.Code = iphoneNoteNoBridge, "bridge_missing"
	} else {
		links := iphones.Devices(ctx)
		p.Devices = make([]proto.IPhoneDevice, len(links))
		var wg sync.WaitGroup
		for i, l := range links {
			wg.Add(1)
			go func() { // each iPhone answers on its own session, at the same time
				defer wg.Done()
				p.Devices[i] = iphoneProbeDevice(ctx, l, false)
			}()
		}
		wg.Wait()
		if !slices.ContainsFunc(p.Devices, func(d proto.IPhoneDevice) bool { return d.Paired }) {
			p.Note, p.Code = iphoneNoteNonePaired, "none_paired"
		}
	}
	if j := iphones.Job(); j != nil {
		p.Job = &proto.ComputerJob{Kind: j.Kind, State: j.State, Step: j.Step, Error: j.Error, Device: j.Device, Started: j.Started}
		n, ok := iphoneNotes[j.ErrorCode]
		switch j.Kind {
		case "install":
			n, ok = iphoneInstallErrors[j.ErrorCode]
		case "prepare":
			if pn, pok := iphonePrepareErrors[j.ErrorCode]; pok {
				n, ok = pn, true // else a code the phone's notes word (developer_mode_off, locked, …)
			}
		}
		if ok {
			p.Job.Error = n // else the job's own words
		}
		// A pairing's step in words the hub translates, and what it waits for at the phone as a code; once failed, why.
		switch {
		case j.State == "running" && j.Kind == "pair":
			p.Job.Code = j.Waiting
			switch j.Waiting {
			case "trust_pending":
				p.Job.Step = iphoneNotePairTrust
			case "locked":
				p.Job.Step = iphoneNotePairUnlock
			default:
				p.Job.Step = iphoneNotePairing
			}
		case j.State == "running" && j.Kind == "install":
			if n, ok := iphoneInstallSteps[j.Stage]; ok {
				p.Job.Step = n
			}
		case j.State == "running" && j.Kind == "prepare":
			p.Job.Step = iphonePrepareStep
		case j.State == "failed":
			p.Job.Code = cmp.Or(j.ErrorCode, "failed")
		}
	}
	p.Checked = time.Now().UTC() // last, as computerStatus: the manager keeps the latest answer
	return p
}

// iphoneInfo is what the bridge's device_info says of an iPhone (the fields status reads).
type iphoneInfo struct {
	Name          string            `json:"name"`
	ProductType   string            `json:"product_type"`
	OSVersion     string            `json:"os_version"`
	Locked        *bool             `json:"locked"`
	DeveloperMode *bool             `json:"developer_mode"`
	Capabilities  map[string]bool   `json:"capabilities"`
	Notes         map[string]string `json:"notes"`
}

// iphoneNeeds are the capabilities the assistant needs of an iPhone to operate it. The element tree (ax_tree) is not
// one: a real iPhone never has it (only the simulator's bridge reads one), and the core then works from the
// screenshot by coordinates, every input asking the user (#3891). Its absence stays in Capabilities, as it is.
var iphoneNeeds = []string{"screenshot", "input"}

// iphoneProbeDevice is one iPhone's status: its link, and for a connected one what device_info says: the session's
// latest, or (fresh, or when it has none) asked now.
func iphoneProbeDevice(ctx context.Context, l iphoneLink, fresh bool) proto.IPhoneDevice {
	d := proto.IPhoneDevice{ID: l.ID, Name: l.Name, ProductType: l.ProductType, IOS: l.IOS, Paired: l.Paired, USB: l.USB,
		State: l.State, Transport: l.Transport, DeveloperMode: l.DeveloperMode, AutoPrepare: l.AutoPrepare}
	switch {
	case !l.Paired && l.USB:
		d.Note, d.Code = iphoneNoteUSB, "usb_unpaired"
		return d
	case !l.Paired:
		return d
	case l.State == "idle":
		// Asked about while the extension is on: its session starts, so the next look finds it connected. Starting it
		// sends nothing to the iPhone but the session's own checks.
		_ = iphones.Connect(l.ID)
		d.Note, d.Code = iphoneNoteConnecting, "connecting"
		return d
	case l.State == "connecting":
		d.Note, d.Code = iphoneNoteConnecting, "connecting"
		return d
	case l.State != "connected": // backoff after a failure
		d.Note, d.Code = iphoneNote(l.ErrorCode), iphoneCode(l.ErrorCode)
		return d
	}
	raw := l.Info
	if fresh || len(raw) == 0 {
		pctx, cancel := context.WithTimeout(ctx, iphoneProbe)
		defer cancel()
		var err error
		if raw, err = iphones.Call(pctx, l.ID, "device_info", json.RawMessage(`{}`)); err != nil {
			code := iphoneErrorCode(err)
			if code == "" && pctx.Err() != nil {
				code = "timeout"
			}
			d.Note, d.Code = iphoneNote(code), iphoneCode(code)
			return d
		}
	}
	var info iphoneInfo
	if json.Unmarshal(raw, &info) != nil {
		d.Note, d.Code = iphoneNoteFailed, "failed"
		return d
	}
	d.Connected = true
	d.Name, d.ProductType, d.IOS = cmp.Or(info.Name, d.Name), cmp.Or(info.ProductType, d.ProductType), cmp.Or(info.OSVersion, d.IOS)
	d.Locked = info.Locked != nil && *info.Locked
	d.Capabilities = info.Capabilities
	if info.DeveloperMode != nil {
		d.DeveloperMode = info.DeveloperMode
	}
	for _, c := range iphoneNeeds {
		if info.Capabilities != nil && !info.Capabilities[c] {
			d.Note, d.Code = iphoneNote(info.Notes[c]), iphoneCode(info.Notes[c])
			return d
		}
	}
	if d.Locked {
		d.Note, d.Code = iphoneNote("locked"), "locked"
		return d
	}
	d.Ready = true
	return d
}

// Install starts installing the bridge from the release's build the manager found (iphone.InstallSource), in the
// background; its state comes back as the job (kind install).
func Install(ctx context.Context, params json.RawMessage) (proto.IPhone, error) {
	var src iphone.InstallSource
	if err := json.Unmarshal(params, &src); err != nil {
		return proto.IPhone{}, fmt.Errorf("iphone.install: %w", err)
	}
	if err := iphones.Install(src); err != nil {
		return proto.IPhone{}, err
	}
	iphoneBridgeSeen.Lock()
	iphoneBridgeSeen.at = time.Time{} // the next status finds the new one
	iphoneBridgeSeen.Unlock()
	return Status(ctx), nil
}

// Call runs one call of the iphone program: status answers here, look and do go to the driver with the bridge
// on that iPhone. Its errors carry whether the step may have reached the iPhone (proto.IPhoneErrorData).
func Call(ctx context.Context, params json.RawMessage) (*proto.IPhoneResult, error) {
	var p proto.IPhoneCall
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, iphoneErr("bad_args", "iphone.call: "+err.Error(), proto.IPhoneNotSent)
	}
	if !iphoneOps[p.Op] {
		return nil, iphoneErr("bad_op", fmt.Sprintf("iphone.call: %q is not status, look or do", p.Op), proto.IPhoneNotSent)
	}
	args := json.RawMessage(strings.TrimSpace(string(p.Args)))
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	if args[0] != '{' || !json.Valid(args) {
		return nil, iphoneErr("bad_args", "iphone.call: args must be an object", proto.IPhoneNotSent)
	}
	// A look or a do at a named iPhone goes straight to its session, which knows whether it is paired and whether
	// there is a bridge; the rest needs the list.
	link := iphoneLink{ID: p.Device, Paired: true}
	if p.Op == "status" || p.Device == "" {
		var err error
		if link, err = iphoneDevice(ctx, p.Device); err != nil {
			return nil, err
		}
	}
	timeout := iphoneCallDefault
	if p.TimeoutMS > 0 {
		ms := min(max(p.TimeoutMS, int(iphoneCallLeast/time.Millisecond)), int(iphoneCallMost/time.Millisecond))
		timeout = time.Duration(ms) * time.Millisecond
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if p.Op == "status" {
		out, _ := json.Marshal(iphoneProbeDevice(cctx, link, true))
		return &proto.IPhoneResult{Output: out}, nil
	}
	dir, err := os.MkdirTemp("", "persona-iphone-")
	if err != nil {
		return nil, iphoneErr("failed", err.Error(), proto.IPhoneNotSent)
	}
	defer os.RemoveAll(dir)
	var acted atomic.Bool // an operation that changes the iPhone may have reached it
	bridge := func(ctx context.Context, op string, args json.RawMessage) (json.RawMessage, error) {
		out, err := iphones.Call(ctx, link.ID, op, args)
		if !iphoneReadOnly[op] && (err == nil || iphoneSent(err) != proto.IPhoneNotSent) {
			acted.Store(true)
		}
		return out, err
	}
	out, err := iphoneCore.Run(cctx, iphoneRun{Device: link.ID, Session: p.Session, Op: p.Op, Args: args, Approved: p.Approved,
		Confirm: p.Confirm, SelfApps: p.SelfApps, SelfNames: p.SelfNames, Bridge: bridge, TempDir: dir})
	if err != nil {
		return nil, iphoneStepErr(ctx, cctx, p.Op, err, timeout, acted.Load())
	}
	res := &proto.IPhoneResult{Output: out.Output, Text: out.Text, NeedsApproval: out.NeedsApproval,
		Approval: out.Approval, ApprovalID: out.ApprovalID}
	if f := iphoneImageIn(dir, out.ImageFile); f != "" {
		res.Image = nodekit.ScreenshotImage(f)
	}
	return res, nil
}

// iphoneImageIn is file when it is a file in dir (a name alone is in it), symlinks resolved, else "": the driver's
// screenshot is read only from the call's own directory.
func iphoneImageIn(dir, file string) string {
	if file == "" {
		return ""
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ""
	}
	realFile, err := filepath.EvalSymlinks(file)
	if err != nil {
		return ""
	}
	if rel, err := filepath.Rel(realDir, realFile); err != nil || !filepath.IsLocal(rel) {
		return ""
	}
	return realFile
}

// iphoneReadOnly are the bridge's operations that change nothing on the iPhone.
var iphoneReadOnly = map[string]bool{"device_info": true, "screenshot": true, "ax_tree": true, "list_apps": true}

// iphoneDevice is the paired iPhone a call names, or the only one when it names none.
func iphoneDevice(ctx context.Context, id string) (iphoneLink, error) {
	if installed, _ := iphones.Bridge(ctx); !installed {
		return iphoneLink{}, iphoneErr("bridge_missing", errNoIPhoneBridge.Message, proto.IPhoneNotSent)
	}
	var paired []iphoneLink
	for _, l := range iphones.Devices(ctx) {
		if l.Paired {
			paired = append(paired, l)
		}
	}
	if id != "" {
		if i := slices.IndexFunc(paired, func(l iphoneLink) bool { return l.ID == id }); i >= 0 {
			return paired[i], nil
		}
		return iphoneLink{}, iphoneErr("not_paired", fmt.Sprintf("iphone.call: no iPhone %q is paired with this computer", id), proto.IPhoneNotSent)
	}
	switch len(paired) {
	case 1:
		return paired[0], nil
	case 0:
		return iphoneLink{}, iphoneErr("not_paired", "iphone.call: no iPhone is paired with this computer", proto.IPhoneNotSent)
	}
	ids := make([]string, len(paired))
	for i, l := range paired {
		ids[i] = l.ID
	}
	return iphoneLink{}, iphoneErr("ambiguous_device", "iphone.call: several iPhones are paired with this computer, name one: "+strings.Join(ids, ", "), proto.IPhoneNotSent)
}

// iphoneErrorCode is the code err carries: an *rpc.Error's, or one with ErrorCode (the host's own errors).
func iphoneErrorCode(err error) string {
	var coded *rpc.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	var withCode interface{ ErrorCode() string }
	if errors.As(err, &withCode) {
		return withCode.ErrorCode()
	}
	return ""
}

// iphoneSent is what err says of whether its operation reached the iPhone: its data's word, else its code's (a
// refusal before acting was not sent), else that it may have been.
func iphoneSent(err error) string {
	var coded *rpc.Error
	if errors.As(err, &coded) && len(coded.Data) > 0 {
		var d proto.IPhoneErrorData
		if json.Unmarshal(coded.Data, &d) == nil && (d.Sent == proto.IPhoneNotSent || d.Sent == proto.IPhoneMaybeSent) {
			return d.Sent
		}
	}
	if slices.Contains(iphoneNotSentCodes, iphoneErrorCode(err)) {
		return proto.IPhoneNotSent
	}
	return proto.IPhoneMaybeSent
}

// iphoneStepErr is the driver's error as the manager gets it, with its code and whether the step may have reached the
// iPhone: not when it says so (iphoneSent) and nothing that changes the iPhone was handed to it during the step
// (acted). A step that ran out of its time, was cancelled (the manager gave up, the connection dropped) or failed
// otherwise may have been done.
func iphoneStepErr(ctx, cctx context.Context, op string, err error, timeout time.Duration, acted bool) error {
	code, msg := iphoneErrorCode(err), err.Error()
	var coded *rpc.Error
	if errors.As(err, &coded) {
		msg = coded.Message
	}
	sent := iphoneSent(err)
	if acted {
		sent = proto.IPhoneMaybeSent
	}
	switch {
	case sent == proto.IPhoneNotSent:
	case ctx.Err() != nil:
		code, msg = "cancelled", "cancelled before the iPhone answered"
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		code, msg = "timeout", "no answer within "+timeout.String()
	}
	if code == "" {
		code = "failed"
	}
	return iphoneErr(code, "iPhone "+op+": "+nodekit.FirstLine(msg), sent)
}

func iphoneErr(code, msg, sent string) error {
	data, _ := json.Marshal(proto.IPhoneErrorData{Sent: sent})
	return &rpc.Error{Code: code, Message: msg, Data: data}
}

// Pair starts pairing or forgetting an iPhone (the iphone program's pair and forget), then answers the status.
func Pair(ctx context.Context, method string, params json.RawMessage) (proto.IPhone, error) {
	var p struct {
		Device string `json:"device"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Device == "" {
		return proto.IPhone{}, fmt.Errorf("iphone.%s: need a device", method)
	}
	start := iphones.Pair
	switch method {
	case "forget":
		start = iphones.Forget
	case "prepare":
		start = iphones.Prepare
	}
	if err := start(p.Device); err != nil {
		return proto.IPhone{}, err
	}
	return Status(ctx), nil
}

// Cancel stops the running pair or forget job.
func Cancel() { iphones.Cancel() }
