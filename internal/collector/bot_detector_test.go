package collector

import (
	"testing"
)

func TestDetectBot(t *testing.T) {
	tests := []struct {
		name       string
		ua         string
		eventName  string
		signals    *ClientSignals
		wantBot    bool
		wantReason string
	}{
		{
			name:       "Legitimate Chrome User",
			ua:         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			eventName:  "page_view",
			signals:    &ClientSignals{Webdriver: false, ScreenW: 1920, ScreenH: 1080, TimeOnPageMs: 5000},
			wantBot:    false,
			wantReason: "",
		},
		{
			name:       "Empty User-Agent",
			ua:         "",
			eventName:  "page_view",
			signals:    nil,
			wantBot:    true,
			wantReason: "empty_user_agent",
		},
		{
			name:       "WhatsApp Preview Bot",
			ua:         "WhatsApp/2.21.12.21 A",
			eventName:  "page_view",
			signals:    nil,
			wantBot:    true,
			wantReason: "social_preview",
		},
		{
			name:       "Facebook External Hit",
			ua:         "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
			eventName:  "page_view",
			signals:    nil,
			wantBot:    true,
			wantReason: "social_preview",
		},
		{
			name:       "Googlebot Crawler",
			ua:         "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			eventName:  "page_view",
			signals:    nil,
			wantBot:    true,
			wantReason: "search_crawler",
		},
		{
			name:       "Ahrefs SEO Scraper",
			ua:         "Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)",
			eventName:  "page_view",
			signals:    nil,
			wantBot:    true,
			wantReason: "seo_scraper",
		},
		{
			name:       "cURL CLI Tool",
			ua:         "curl/7.88.1",
			eventName:  "page_view",
			signals:    nil,
			wantBot:    true,
			wantReason: "http_tool",
		},
		{
			name:       "Python Requests Tool",
			ua:         "python-requests/2.31.0",
			eventName:  "page_view",
			signals:    nil,
			wantBot:    true,
			wantReason: "http_tool",
		},
		{
			name:       "Headless Chrome UA",
			ua:         "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/120.0.0.0 Safari/537.36",
			eventName:  "page_view",
			signals:    nil,
			wantBot:    true,
			wantReason: "headless_automation",
		},
		{
			name:      "Client Webdriver Active",
			ua:        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			eventName: "page_view",
			signals: &ClientSignals{
				Webdriver: true,
			},
			wantBot:    true,
			wantReason: "client_webdriver",
		},
		{
			name:      "Client Headless Active",
			ua:        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			eventName: "page_view",
			signals: &ClientSignals{
				Headless: true,
			},
			wantBot:    true,
			wantReason: "client_headless",
		},
		{
			name:      "Fast Form Fill Lead",
			ua:        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			eventName: "lead",
			signals: &ClientSignals{
				ScreenW:      1920,
				ScreenH:      1080,
				TimeOnPageMs: 350,
			},
			wantBot:    true,
			wantReason: "fast_form_fill",
		},
		{
			name:      "Zero Screen Dimension",
			ua:        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			eventName: "page_view",
			signals: &ClientSignals{
				ScreenW:      0,
				ScreenH:      0,
				TimeOnPageMs: 1500,
			},
			wantBot:    true,
			wantReason: "zero_screen_dimension",
		},
		{
			name:      "Legitimate Human Lead",
			ua:        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			eventName: "lead",
			signals: &ClientSignals{
				ScreenW:      1920,
				ScreenH:      1080,
				TimeOnPageMs: 14500,
			},
			wantBot:    false,
			wantReason: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBot, gotReason := DetectBot(tt.ua, tt.eventName, tt.signals)
			if gotBot != tt.wantBot {
				t.Errorf("DetectBot() isBot = %v, want %v", gotBot, tt.wantBot)
			}
			if gotReason != tt.wantReason {
				t.Errorf("DetectBot() botReason = %v, want %v", gotReason, tt.wantReason)
			}
		})
	}
}
