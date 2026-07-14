package alert

// Renderer turns a provider-neutral Alert into a delivery payload. Slack is the
// only implementation in v1; Discord / generic webhooks slot in here later
// without touching the state machine, routing, or checks.
type Renderer interface {
	// Render produces the request body for one target. mention is non-empty only
	// for PAGE alerts on a target that configured one.
	Render(a Alert, mention string) ([]byte, error)
	// ContentType is the request Content-Type header value.
	ContentType() string
}
