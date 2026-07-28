package policy

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/settings"
)

const (
	MaximumTemplateBytes = 4 * 1024
	MaximumOutputBytes   = 16 * 1024
)

var templatePlaceholderPattern = regexp.MustCompile(
	`\{\{\s*(sourceName|event|status|project|priority|occurredAt|runtime|surface)\s*\}\}`,
)

type TemplateInput struct {
	SourceName string
	Event      string
	Status     string
	Project    string
	Priority   string
	OccurredAt time.Time
	Runtime    string
	Surface    string
}

// InputFromNotification deliberately copies only metadata-only fields. CWD,
// summary, transcript, prompt, and code are absent from TemplateInput.
func InputFromNotification(
	notification event.Notification,
	sourceName string,
) (TemplateInput, error) {
	if err := notification.Validate(); err != nil {
		return TemplateInput{}, err
	}
	if notification.PrivacyLevel != event.PrivacyMetadataOnly {
		return TemplateInput{}, errors.New(
			"template input must be metadata-only",
		)
	}
	return TemplateInput{
		SourceName: sourceName,
		Event:      notification.Event,
		Status:     notification.Status,
		Project:    notification.Project,
		Priority:   notification.Priority,
		OccurredAt: notification.OccurredAt,
		Runtime:    notification.Runtime,
		Surface:    notification.Surface,
	}, nil
}

func Preview(body string, input TemplateInput) (string, error) {
	return Render(settings.Template{ID: "preview", Body: body}, input)
}

func Render(
	template settings.Template,
	input TemplateInput,
) (string, error) {
	if !utf8.ValidString(template.Body) {
		return "", errors.New("template must be valid UTF-8")
	}
	if len(template.Body) > MaximumTemplateBytes {
		return "", fmt.Errorf(
			"template exceeds %d bytes",
			MaximumTemplateBytes,
		)
	}
	if err := template.Validate(); err != nil {
		return "", err
	}
	if input.OccurredAt.IsZero() {
		return "", errors.New("template input occurredAt is required")
	}
	values := map[string]string{
		"sourceName": input.SourceName,
		"event":      input.Event,
		"status":     input.Status,
		"project":    input.Project,
		"priority":   input.Priority,
		"occurredAt": input.OccurredAt.UTC().Format(time.RFC3339Nano),
		"runtime":    input.Runtime,
		"surface":    input.Surface,
	}
	for name, value := range values {
		if !utf8.ValidString(value) {
			return "", fmt.Errorf(
				"template input %s must be valid UTF-8",
				name,
			)
		}
	}
	rendered := templatePlaceholderPattern.ReplaceAllStringFunc(
		template.Body,
		func(match string) string {
			parts := templatePlaceholderPattern.FindStringSubmatch(match)
			return values[parts[1]]
		},
	)
	if len(rendered) > MaximumOutputBytes {
		return "", fmt.Errorf(
			"rendered template exceeds %d bytes",
			MaximumOutputBytes,
		)
	}
	if !utf8.ValidString(rendered) {
		return "", errors.New("rendered template must be valid UTF-8")
	}
	return strings.Clone(rendered), nil
}
