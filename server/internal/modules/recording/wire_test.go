package recording

import "github.com/nwverse/persona/server/internal/assistantwire"

// The wire names the fake hub of these tests uses (assistantwire's, as the assistant names them).
type (
	AppCall        = assistantwire.AppCall
	Broadcast      = assistantwire.Broadcast
	Device         = assistantwire.Device
	DeviceCall     = assistantwire.DeviceCall
	FeishuStatus   = assistantwire.FeishuStatus
	KBSettings     = assistantwire.KBSettings
	ModelBinding   = assistantwire.ModelBinding
	NotifyResult   = assistantwire.NotifyResult
	NotifyRequest  = assistantwire.NotifyRequest
	Setting        = assistantwire.Setting
	Snapshot       = assistantwire.Snapshot
	SnapshotResult = assistantwire.SnapshotResult
)

const (
	AppChannels            = assistantwire.AppChannels
	AppConnectors          = assistantwire.AppConnectors
	AppFeishu              = assistantwire.AppFeishu
	AppInteractions        = assistantwire.AppInteractions
	AppPlugins             = assistantwire.AppPlugins
	AppSettings            = assistantwire.AppSettings
	AppTool                = assistantwire.AppTool
	ChannelsList           = assistantwire.ChannelsList
	ConnectorsSignedIn     = assistantwire.ConnectorsSignedIn
	ConnectorsSnapshots    = assistantwire.ConnectorsSnapshots
	DeviceAll              = assistantwire.DeviceAll
	DeviceAssistantPrepare = assistantwire.DeviceAssistantPrepare
	DeviceAssistantWarm    = assistantwire.DeviceAssistantWarm
	DeviceDevicesList      = assistantwire.DeviceDevicesList
	DeviceInteractionsList = assistantwire.DeviceInteractionsList
	DeviceLaunchersList    = assistantwire.DeviceLaunchersList
	DeviceLaunchersProbe   = assistantwire.DeviceLaunchersProbe
	DeviceSessionGet       = assistantwire.DeviceSessionGet
	DeviceSessionRename    = assistantwire.DeviceSessionRename
	DeviceSessionSendOnce  = assistantwire.DeviceSessionSendOnce
	DeviceSessionsList     = assistantwire.DeviceSessionsList
	DeviceSessionStart     = assistantwire.DeviceSessionStart
	DeviceWorktreeCreate   = assistantwire.DeviceWorktreeCreate
	DeviceWorktreePrepare  = assistantwire.DeviceWorktreePrepare
	FeishuStatusGet        = assistantwire.FeishuStatusGet
	PluginsAssistant       = assistantwire.PluginsAssistant
	RoleHeader             = assistantwire.RoleHeader
	SettingsList           = assistantwire.SettingsList
)
