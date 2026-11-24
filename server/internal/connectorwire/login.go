package connectorwire

import "context"

// Field is one thing a sign-in step asks for.
type Field struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Secret: a password; the app hides what is typed.
	Secret      bool   `json:"secret,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

// PromptDeviceCode is a step that shows a code to enter on a web page (URL) and is asked again every Interval
// seconds until the user has (GitHub's device flow).
const PromptDeviceCode = "device_code"

// PromptQRCode is a step that shows URL as a QR code to scan with another app (WeChat) and is asked again every
// Interval seconds until the answer is another step or Done. Opening URL as a link does nothing useful: the app draws
// the code from QR, which the hub adds.
const PromptQRCode = "qr"

// QRCode is a QR code's modules: Size rows of Size "0" / "1", without the quiet zone; the app draws them in its own
// colors.
type QRCode struct {
	Size int      `json:"size"`
	Rows []string `json:"rows"`
}

// Prompt is where a sign-in stands: the step to fill in next, or Done.
type Prompt struct {
	Step   string  `json:"step,omitempty"`
	Fields []Field `json:"fields,omitempty"`
	// Kind is "" for fields to fill in, PromptDeviceCode for a code: UserCode to enter at URL, PromptQRCode for URL
	// as a QR code to scan; for both the app sends the same step (no values) every Interval seconds until the answer
	// is another step or Done.
	Kind     string `json:"kind,omitempty"`
	URL      string `json:"url,omitempty"`
	UserCode string `json:"user_code,omitempty"`
	Interval int    `json:"interval,omitempty"`
	// QR is URL's QR code for PromptQRCode, and for a PromptDeviceCode whose URL carries UserCode as its user_code
	// (scanning it needs nothing typed): the hub fills it in on the way to the app (a connector leaves it out).
	QR *QRCode `json:"qr,omitempty"`
	// Hint is for people: where the code went ("The code was sent to your other Telegram device"), the password's hint.
	Hint string `json:"hint,omitempty"`
	// Link is an absolute URL where what this step asks for is done ("the provider's console, to create the app");
	// LinkText is the button's label. Empty Link means no link (an old hub, or a step that needs none). The app opens
	// it in a new tab; it never navigates itself there.
	Link     string `json:"link,omitempty"`
	LinkText string `json:"link_text,omitempty"`
	// Error is what was wrong with the values just given ("Wrong code"), for people: the same step is asked again.
	Error string `json:"error,omitempty"`
	Done  bool   `json:"done,omitempty"`
	// Account is who is signed in, once Done.
	Account string `json:"account,omitempty"`
}

// Login is a connector the user signs in to from the app, in steps (Telegram: phone number, code, 2FA password; a
// token is a single step). LoginStep with step "" starts over; with a step and its values it goes on. A mistake the
// user can fix comes back as Prompt.Error with the same step; an error is for what should not happen. What signing
// in yields (a session, a token) goes to the connector's Secrets: never into a Prompt, a Status or a log.
type Login interface {
	LoginStep(ctx context.Context, step string, values map[string]string) (Prompt, error)
	Logout(ctx context.Context) error
}

// Secrets keeps one connector's secret (a session, a token) for it, sealed in the hub's database
// (connector_secrets). Load gives nil when nothing is stored. The hub hands each connector its own: one cannot read
// another's.
type Secrets interface {
	Load(ctx context.Context) ([]byte, error)
	Save(ctx context.Context, data []byte) error
	Delete(ctx context.Context) error
}
