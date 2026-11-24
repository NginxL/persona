package feishubot

// The bot's card and event model lives in internal/botcard (pure, no I/O); these names keep the package's callers
// compiling while they move to botcard (#13404).

import "github.com/nwverse/persona/server/internal/botcard"

type (
	APIError          = botcard.APIError
	Answer            = botcard.Answer
	AnswerPart        = botcard.AnswerPart
	BotInfo           = botcard.BotInfo
	Button            = botcard.Button
	Card              = botcard.Card
	CardAction        = botcard.CardAction
	CardPayload       = botcard.CardPayload
	CardResponse      = botcard.CardResponse
	ConfigureIssue    = botcard.ConfigureIssue
	ConfigureReport   = botcard.ConfigureReport
	ConnState         = botcard.ConnState
	Content           = botcard.Content
	CotEvent          = botcard.CotEvent
	Envelope          = botcard.Envelope
	Handler           = botcard.Handler
	Item              = botcard.Item
	Media             = botcard.Media
	Mention           = botcard.Mention
	MenuItem          = botcard.MenuItem
	MessageEvent      = botcard.MessageEvent
	Option            = botcard.Option
	P2PEntered        = botcard.P2PEntered
	Pin               = botcard.Pin
	Recalled          = botcard.Recalled
	RegisterChallenge = botcard.RegisterChallenge
	RegisterTicket    = botcard.RegisterTicket
	Toast             = botcard.Toast
	UserID            = botcard.UserID
)

const (
	AccountsFeishu     = botcard.AccountsFeishu
	AccountsLark       = botcard.AccountsLark
	Blue               = botcard.Blue
	Danger             = botcard.Danger
	Default            = botcard.Default
	DomainFeishu       = botcard.DomainFeishu
	DomainLark         = botcard.DomainLark
	EventCardAction    = botcard.EventCardAction
	EventMessage       = botcard.EventMessage
	EventP2PEntered    = botcard.EventP2PEntered
	EventRecalled      = botcard.EventRecalled
	Green              = botcard.Green
	Grey               = botcard.Grey
	IssueBotCapability = botcard.IssueBotCapability
	IssueIncompatible  = botcard.IssueIncompatible
	IssueNoApp         = botcard.IssueNoApp
	IssueNone          = botcard.IssueNone
	IssueNotSelfBuilt  = botcard.IssueNotSelfBuilt
	IssuePatchScope    = botcard.IssuePatchScope
	IssueUnderReview   = botcard.IssueUnderReview
	IssueUnreachable   = botcard.IssueUnreachable
	Neutral            = botcard.Neutral
	Orange             = botcard.Orange
	Primary            = botcard.Primary
	Purple             = botcard.Purple
	Red                = botcard.Red
	RegisterDenied     = botcard.RegisterDenied
	RegisterDone       = botcard.RegisterDone
	RegisterExpired    = botcard.RegisterExpired
	RegisterFailed     = botcard.RegisterFailed
	RegisterPending    = botcard.RegisterPending
	RegisterSlowDown   = botcard.RegisterSlowDown
	TypeFile           = botcard.TypeFile
	TypeInteractive    = botcard.TypeInteractive
	TypePost           = botcard.TypePost
	TypeText           = botcard.TypeText
	Wathet             = botcard.Wathet
)

var (
	AICommand            = botcard.AICommand
	AccountsHost         = botcard.AccountsHost
	AppConsoleURL        = botcard.AppConsoleURL
	Btn                  = botcard.Btn
	CardEntityContent    = botcard.CardEntityContent
	CardMarkdown         = botcard.CardMarkdown
	CardRejected         = botcard.CardRejected
	ChatRefused          = botcard.ChatRefused
	Code                 = botcard.Code
	Confirm              = botcard.Confirm
	ConsoleURL           = botcard.ConsoleURL
	CotFinish            = botcard.CotFinish
	CotParagraph         = botcard.CotParagraph
	CotStart             = botcard.CotStart
	CotToolCall          = botcard.CotToolCall
	CotToolResult        = botcard.CotToolResult
	CredentialRejected   = botcard.CredentialRejected
	ErrorCode            = botcard.ErrorCode
	EscapeMarkdown       = botcard.EscapeMarkdown
	InlineAnswer         = botcard.InlineAnswer
	InlineTag            = botcard.InlineTag
	Input                = botcard.Input
	LinkBtn              = botcard.LinkBtn
	LocalTime            = botcard.LocalTime
	MarkdownBlock        = botcard.MarkdownBlock
	MessageGone          = botcard.MessageGone
	MsTime               = botcard.MsTime
	NewCard              = botcard.NewCard
	Overflow             = botcard.Overflow
	PageBtn              = botcard.PageBtn
	ParseContent         = botcard.ParseContent
	ParseEnvelope        = botcard.ParseEnvelope
	PersonaCards         = botcard.PersonaCards
	PostMarkdown         = botcard.PostMarkdown
	RateLimited          = botcard.RateLimited
	ReadCommand          = botcard.ReadCommand
	Refused              = botcard.Refused
	RenderAnswer         = botcard.RenderAnswer
	RenderAnswerWith     = botcard.RenderAnswerWith
	ResolveDomain        = botcard.ResolveDomain
	Select               = botcard.Select
	SidebarURL           = botcard.SidebarURL
	Split                = botcard.Split
	StreamClosed         = botcard.StreamClosed
	StripLeadingMentions = botcard.StripLeadingMentions
	Submit               = botcard.Submit
	TextBtn              = botcard.TextBtn
	TextContent          = botcard.TextContent
	TokenGone            = botcard.TokenGone
	Transient            = botcard.Transient
	ErrTooLarge          = botcard.ErrTooLarge
)

// Unexported names of the moved helpers the client still uses.
var (
	tokenGone = botcard.TokenGone
	msTime    = botcard.MsTime
	errorCode = botcard.ErrorCode
)
