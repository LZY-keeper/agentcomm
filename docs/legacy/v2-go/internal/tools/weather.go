package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agentcomm/internal/types"
)

// RegisterWeather queries open-meteo (no API key required). Offline-safe: on
// network failure it returns a clear degraded message instead of erroring, so
// demos without internet still respond gracefully.
func RegisterWeather(r *Registry) {
	schema := types.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"city": map[string]any{"type": "string", "description": "城市名，如 北京 / Beijing"},
		},
		Required: []string{"city"},
	}
	r.Register("get_weather", "查询城市天气 weather forecast 温度 风速", schema,
		func(ctx context.Context, args map[string]any) (types.ToolResult, error) {
			city := stringArg(args, "city", "location", "q")
			if city == "" {
				return types.ToolResult{}, fmt.Errorf("missing city")
			}
			text, err := weather(ctx, city)
			if err != nil {
				return types.ToolResult{Content: []types.ToolContent{{Type: types.ContentText,
					Text: fmt.Sprintf("[离线降级] 无法查询 %s 的实时天气：%v", city, err)}}}, nil
			}
			return types.ToolResult{Content: []types.ToolContent{{Type: types.ContentText, Text: text}}}, nil
		},
		WithInputTypes("location", "text"), WithOutputTypes("weather"))
}

func weather(ctx context.Context, city string) (string, error) {
	client := &http.Client{Timeout: 6 * time.Second}
	geoURL := "https://geocoding-api.open-meteo.com/v1/search?name=" + url.QueryEscape(city) + "&count=1&language=zh"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geoURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var geo struct {
		Results []struct {
			Name string  `json:"name"`
			Lat  float64 `json:"latitude"`
			Lon  float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &geo); err != nil {
		return "", err
	}
	if len(geo.Results) == 0 {
		return "", fmt.Errorf("city not found")
	}
	g := geo.Results[0]
	fcURL := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,wind_speed_10m", g.Lat, g.Lon)
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, fcURL, nil)
	resp2, err := client.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()
	b2, _ := io.ReadAll(resp2.Body)
	var fc struct {
		Current struct {
			Temp      float64 `json:"temperature_2m"`
			WindSpeed float64 `json:"wind_speed_10m"`
		} `json:"current"`
	}
	_ = json.Unmarshal(b2, &fc)
	return strings.TrimSpace(fmt.Sprintf("%s：当前气温 %.1f°C，风速 %.1f km/h", g.Name, fc.Current.Temp, fc.Current.WindSpeed)), nil
}
