package alert

import (
	"encoding/json"
	"fmt"

	"github.com/akaike-byob/dokploy-sentinel/internal/config"
)

// Severity bar colors (docs/plan/05-alerting.md §5.2).
const (
	colorWARN     = "#ECB22E"
	colorALERT    = "#E8912D"
	colorPAGE     = "#E01E5A"
	colorRESOLVED = "#2EB67D"
	colorDEGRADED = "#8D8D8D"
)

// SlackRenderer renders an Alert as attachment-wrapped Block Kit: the attachment
// gives the colored severity bar, the blocks give the layout, and the top-level
// text is the self-contained phone-push line.
type SlackRenderer struct{}

// ContentType is application/json for Slack incoming webhooks.
func (SlackRenderer) ContentType() string { return "application/json" }

// Render builds the Slack payload for one target.
func (SlackRenderer) Render(a Alert, mention string) ([]byte, error) {
	p := slackPayload{
		Text:        pushLine(a, mention),
		Attachments: []slackAttachment{{Color: color(a), Blocks: blocks(a)}},
	}
	return json.MarshalIndent(p, "", "  ")
}

func blocks(a Alert) []slackBlock {
	bs := []slackBlock{
		{Type: "header", Text: &slackText{Type: "plain_text", Text: headerText(a), Emoji: boolPtr(true)}},
	}

	fields := []slackText{
		{Type: "mrkdwn", Text: "*Host:*\n" + a.Host},
		{Type: "mrkdwn", Text: "*Scope:*\n" + a.Scope},
		{Type: "mrkdwn", Text: "*Measured:*\n" + a.Measured},
	}
	if a.Threshold != "" {
		fields = append(fields, slackText{Type: "mrkdwn", Text: "*Threshold:*\n" + a.Threshold})
	}
	bs = append(bs, slackBlock{Type: "section", Fields: fields})

	if a.Fix != "" && !a.resolved() {
		bs = append(bs, slackBlock{Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: "*Suggested fix:* " + a.Fix}})
	}

	bs = append(bs, slackBlock{Type: "context", Elements: []slackText{
		{Type: "mrkdwn", Text: contextLine(a)},
	}})
	return bs
}

// pushLine is the locked-phone-readable summary in the notification.
func pushLine(a Alert, mention string) string {
	line := fmt.Sprintf("%s %s — %s on %s: %s", emoji(a), label(a), a.Title, a.Host, a.Measured)
	if a.Threshold != "" && !a.resolved() {
		line += fmt.Sprintf(" (threshold %s)", a.Threshold)
	}
	if mention != "" {
		line = mention + " " + line
	}
	return line
}

func headerText(a Alert) string {
	if a.Kind == KindEscalate {
		return fmt.Sprintf("%s ESCALATED · %s — %s", emoji(a), a.Tier, a.Title)
	}
	return fmt.Sprintf("%s %s — %s", emoji(a), label(a), a.Title)
}

func contextLine(a Alert) string {
	return fmt.Sprintf("check=`%s` · key=`%s` · %s · run #%d",
		a.Check, a.Key, a.Timestamp.UTC().Format("2006-01-02T15:04:05Z"), a.RunID)
}

// label is the word shown for the event: the tier, or RESOLVED/DEGRADED.
func label(a Alert) string {
	switch a.Kind {
	case KindResolved:
		return "RESOLVED"
	case KindDegraded:
		return "MONITORING DEGRADED"
	default:
		return a.Tier.String()
	}
}

func color(a Alert) string {
	switch a.Kind {
	case KindResolved:
		return colorRESOLVED
	case KindDegraded:
		return colorDEGRADED
	}
	switch a.Tier {
	case config.TierPAGE:
		return colorPAGE
	case config.TierALERT:
		return colorALERT
	default:
		return colorWARN
	}
}

func emoji(a Alert) string {
	switch a.Kind {
	case KindResolved:
		return "✅"
	case KindDegraded:
		return "⚪"
	}
	switch a.Tier {
	case config.TierPAGE:
		return "🔴"
	case config.TierALERT:
		return "🟠"
	default:
		return "🟡"
	}
}

func boolPtr(b bool) *bool { return &b }

// ---- Slack Block Kit payload shapes ----

type slackPayload struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

type slackAttachment struct {
	Color  string       `json:"color"`
	Blocks []slackBlock `json:"blocks"`
}

type slackBlock struct {
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Fields   []slackText `json:"fields,omitempty"`
	Elements []slackText `json:"elements,omitempty"`
}

type slackText struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji *bool  `json:"emoji,omitempty"`
}
