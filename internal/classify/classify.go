package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// ClassificationResult is the JSON written to Tigris for each submission.
type ClassificationResult struct {
	ID         string           `json:"id"`
	Text       string           `json:"text"`
	MediaURL   string           `json:"media_url,omitempty"`
	Categories []CategoryResult `json:"categories"`
	Violation  bool             `json:"violation"`
	Confidence float64          `json:"confidence"`
	CreatedAt  time.Time        `json:"created_at"`
}

// CategoryResult holds the moderation decision for a single category.
type CategoryResult struct {
	Name       string  `json:"name"`
	Flagged    bool    `json:"flagged"`
	Confidence float64 `json:"confidence"`
}

const systemPrompt = `You are a content moderation classifier. Analyze the provided content and return a JSON object with exactly this schema:

{"categories": [{"name": "hate_speech", "flagged": BOOL, "confidence": FLOAT}, {"name": "spam", "flagged": BOOL, "confidence": FLOAT}, {"name": "nsfw", "flagged": BOOL, "confidence": FLOAT}, {"name": "harassment", "flagged": BOOL, "confidence": FLOAT}]}

Rules:
- Return ONLY the JSON object. No explanation, no markdown formatting, no code fences.
- "flagged" is true if the content violates that category.
- "confidence" is your certainty in the flagged/not-flagged decision (0.0 = no certainty, 1.0 = absolute certainty).
- Always include all four categories in the order listed above.`

// classifyResponse is the raw JSON shape returned by Claude.
type classifyResponse struct {
	Categories []CategoryResult `json:"categories"`
}

// Classify sends content to Claude for moderation classification and returns
// the structured result.
func Classify(ctx context.Context, client anthropic.Client, id, text, mediaURL string) (*ClassificationResult, error) {
	userContent := fmt.Sprintf("Classify the following content:\n\n%s", text)
	if mediaURL != "" {
		userContent += fmt.Sprintf("\n\nMedia URL: %s", mediaURL)
	}

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeSonnet4_0,
		MaxTokens: 1024,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userContent)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("classify: claude api: %w", err)
	}

	// Extract text from response.
	var rawJSON string
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			rawJSON = tb.Text
			break
		}
	}
	if rawJSON == "" {
		return nil, fmt.Errorf("classify: empty response from claude")
	}

	// Strip markdown fences if present.
	rawJSON = stripCodeFences(rawJSON)

	var cr classifyResponse
	if err := json.Unmarshal([]byte(rawJSON), &cr); err != nil {
		return nil, fmt.Errorf("classify: parse response: %w (raw: %s)", err, rawJSON)
	}

	if len(cr.Categories) == 0 {
		return nil, fmt.Errorf("classify: no categories in response (raw: %s)", rawJSON)
	}

	// Compute aggregate violation and confidence.
	violation := false
	maxFlaggedConf := 0.0
	maxAllConf := 0.0

	for _, cat := range cr.Categories {
		if cat.Confidence > maxAllConf {
			maxAllConf = cat.Confidence
		}
		if cat.Flagged {
			violation = true
			if cat.Confidence > maxFlaggedConf {
				maxFlaggedConf = cat.Confidence
			}
		}
	}

	confidence := maxAllConf
	if violation {
		confidence = maxFlaggedConf
	}

	return &ClassificationResult{
		ID:         id,
		Text:       text,
		MediaURL:   mediaURL,
		Categories: cr.Categories,
		Violation:  violation,
		Confidence: confidence,
		CreatedAt:  time.Now(),
	}, nil
}

// stripCodeFences removes markdown code fences from Claude's response.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}
