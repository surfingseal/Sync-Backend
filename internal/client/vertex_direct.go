package client

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"example.com/sync/internal/config"
	"example.com/sync/internal/model"
	"google.golang.org/genai"
)

const DirectPromptVersion = "direct_music_prompt_v3_mvp"
const DirectMusicPrompt = `You are selecting music for a playlist inspired by this image.
Analyze only its visual atmosphere. Do not infer the user's actual emotions,
personality, mental state, identity or intentions. Descriptions refer to the image.
Understand the image holistically: visual mood, lighting, palette, setting, time,
energy, intimacy/openness, nostalgia/modernity, cinematic feeling and stillness.
Choose music someone would want to hear while viewing this image. Do not restrict
your reasoning to a genre taxonomy. Select a coherent but diverse playlist, not
merely a list of famous hits. Prefer at most two tracks by the same artist.
This MVP selects at most five final tracks after verification. Generate a pool of
12 real candidates when requested, not just five. Aim for at
least six Korean-eligible candidates when image fit supports them. Do not damage
musical coherence to force a ratio. Rank candidates by image fit, strongest first.
Return model-classified lyric_language: ko, en, ko_en, instrumental, or unknown.
ko is Korean-dominant with English phrases/hooks allowed; en is English-dominant;
ko_en has meaningful Korean AND English lyrics. Instrumental has no meaningful
vocal lyrics. Unknown means uncertain or a different dominant language (Japanese,
Spanish, French, etc.). Do not mislabel other languages, use artist nationality,
invent lyric percentages or claim verified lyric metadata. Korean eligibility
is only ko/ko_en; English-only songs by Korean artists are en.
Also return scene.description and playlist.title. scene.description is a concise
Korean noun phrase, one line, usually 10–25 characters, up to 30, no final period.
Prioritize observable scene facts, general location/environment, clear activity/event,
then visually supported time/season; restrained atmosphere is optional.
Example: 가을 밤 불꽃놀이 중인 공원
Do not use 사진 속, 사진에는, 장면은, 보인다, 느껴진다, 분위기다, 모습이다.
Avoid speculative emotion, poetic copywriting and unsupported season/time.
Do not generate specific place names such as 한강공원, 서울숲, 해운대, 광안리,
제주도 without clear landmark/sign/metadata evidence. Prefer general places:
공원, 강변 공원, 해변, 카페, 거리. Do not infer people's emotions or intentions.
playlist.title is a short English playlist name (e.g. Autumn Fireworks, Quiet Focus).
scene.description describes the image, not musical fit; reasons explain music.
Recommend only real, officially released tracks you are reasonably confident exist.
Do not invent artists, titles, collaborations, remixes or alternate versions.
If uncertain a track exists, omit it. Return canonical artist and track names.
Prefer the original official release. Do not recommend karaoke, backing tracks,
unofficial covers, fan uploads, slowed/reverb, sped-up, nightcore or compilations.
Do not include URLs or YouTube video IDs. A downstream resolver verifies identity.
Return a short analysis_summary and ordered tracks with artist, title, fit_score
in [0,1] and a concise reason. Fit scores are relevance signals, not probabilities.
Reasons are explanation only. Do not pad the list with uncertain tracks.`

type DirectTrackRecommender interface {
	RecommendTracks(context.Context, []byte, string, int) (*model.DirectMusicRecommendation, error)
}

type VertexDirectRecommender struct {
	sdk      *genai.Client
	model    string
	thinking genai.ThinkingLevel
	timeout  time.Duration
}

func DirectMusicSchema(count int) map[string]any {
	str := func(n int) map[string]any { return map[string]any{"type": "string", "maxLength": n} }
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"analysis_summary", "scene", "playlist", "tracks"}, "properties": map[string]any{
		"analysis_summary": str(1500),
		"scene":            map[string]any{"type": "object", "additionalProperties": false, "required": []string{"description"}, "properties": map[string]any{"description": str(30)}},
		"playlist":         map[string]any{"type": "object", "additionalProperties": false, "required": []string{"title"}, "properties": map[string]any{"title": str(100)}}, "tracks": map[string]any{"type": "array", "minItems": 1, "maxItems": count, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"artist", "title", "fit_score", "reason", "lyric_language"}, "properties": map[string]any{"lyric_language": map[string]any{"type": "string", "enum": []string{"ko", "en", "ko_en", "instrumental", "unknown"}}, "artist": str(200), "title": str(200), "reason": str(400), "fit_score": map[string]any{"type": "number", "minimum": 0, "maximum": 1}}}}}}
}
func NewVertexDirectRecommender(ctx context.Context, cfg config.Config) (*VertexDirectRecommender, error) {
	if cfg.GoogleCloudProject == "" || cfg.GoogleCloudLocation == "" || cfg.VertexModel == "" || cfg.VertexTimeout <= 0 {
		return nil, ErrAIConfiguration
	}
	if err := config.ValidateThinking(cfg.VertexModel, cfg.VertexThinkingLevel); err != nil {
		return nil, ErrAIConfiguration
	}
	cfg.VertexRetryMode = "budget"
	cfg.VertexRetryAttempts = 1
	cc := vertexClientConfig(cfg)
	sdk, err := genai.NewClient(ctx, cc)
	if err != nil {
		return nil, fmt.Errorf("%w: direct ADC initialization failed", ErrAIConfiguration)
	}
	base := cc.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	cc.HTTPClient.Transport = observedTransport{base: base}
	return &VertexDirectRecommender{sdk, cfg.VertexModel, genai.ThinkingLevel(cfg.VertexThinkingLevel), cfg.VertexTimeout}, nil
}
func (v *VertexDirectRecommender) RecommendTracks(ctx context.Context, data []byte, mime string, count int) (*model.DirectMusicRecommendation, error) {
	if count < 1 || count > 20 || len(data) == 0 || (mime != "image/jpeg" && mime != "image/png" && mime != "image/webp") {
		return nil, ErrInvalidAIResponse
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	ctx, m := WithCallMetrics(ctx)
	start := time.Now()
	defer func() {
		m.mu.Lock()
		m.VertexMS = float64(time.Since(start)) / float64(time.Millisecond)
		m.mu.Unlock()
		if outer, _ := ctx.Value(outerMetricsKey{}).(*CallMetrics); outer != nil {
			s := m.Snapshot()
			outer.mu.Lock()
			outer.Attempts = s.Attempts
			outer.StatusCodes = s.StatusCodes
			outer.VertexMS = s.VertexMS
			outer.OutputBytes = s.OutputBytes
			outer.InputTokens = s.InputTokens
			outer.OutputTokens = s.OutputTokens
			outer.ThoughtTokens = s.ThoughtTokens
			outer.mu.Unlock()
		}
	}()
	response, err := v.sdk.Models.GenerateContent(ctx, v.model, []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: fmt.Sprintf("Recommend up to %d real tracks for this image, strongest image fit first.", count)}, {InlineData: &genai.Blob{Data: data, MIMEType: mime}}}}}, &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: DirectMusicPrompt}}}, ResponseMIMEType: "application/json", ResponseJsonSchema: DirectMusicSchema(count), MaxOutputTokens: 8192, Temperature: genai.Ptr(float32(0.7)), ThinkingConfig: &genai.ThinkingConfig{ThinkingLevel: v.thinking}})
	if err != nil {
		return nil, mapVertexError(ctx, err)
	}
	if response == nil || len(response.Candidates) != 1 || response.Candidates[0] == nil || response.Candidates[0].FinishReason != genai.FinishReasonStop {
		return nil, ErrInvalidAIResponse
	}
	m.mu.Lock()
	m.OutputBytes = len(response.Text())
	if u := response.UsageMetadata; u != nil {
		m.InputTokens = u.PromptTokenCount
		m.OutputTokens = u.CandidatesTokenCount
		m.ThoughtTokens = u.ThoughtsTokenCount
	}
	m.mu.Unlock()
	result, err := model.DecodeDirectRecommendation([]byte(response.Text()), count)
	if err != nil {
		return nil, ErrInvalidAIResponse
	}
	return result, nil
}
