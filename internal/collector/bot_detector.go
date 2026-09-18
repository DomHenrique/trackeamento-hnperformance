package collector

import (
	"regexp"
	"strings"
)

var (
	reSocialPreview = regexp.MustCompile(`(?i)(facebookexternalhit|facebot|whatsapp|twitterbot|telegrambot|slackbot|linkedinbot|pinterestbot|discordbot|vkshare|skypeuripreview)`)
	reSearchCrawler = regexp.MustCompile(`(?i)(googlebot|bingbot|baiduspider|yandexbot|duckduckbot|sogou|exabot|ia_archiver)`)
	reSEOScraper    = regexp.MustCompile(`(?i)(ahrefsbot|semrushbot|dotbot|mj12bot|megaindex|seznambot|screaming frog)`)
	reHTTPTool      = regexp.MustCompile(`(?i)(curl/|wget/|python-requests|python-urllib|go-http-client|node-fetch|axios/|postmanruntime|okhttp|aiohttp|scrapy)`)
	reHeadless      = regexp.MustCompile(`(?i)(headlesschrome|phantomjs|playwright|puppeteer|selenium|cypress)`)
)

// DetectBot avalia o User-Agent, cabeçalhos e sinais comportamentais do SDK para identificar robôs
func DetectBot(userAgent string, eventName string, signals *ClientSignals) (bool, string) {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		return true, "empty_user_agent"
	}

	// 1. Verificação de sinais ativos do cliente (SDK)
	if signals != nil {
		if signals.Webdriver {
			return true, "client_webdriver"
		}
		if signals.Headless {
			return true, "client_headless"
		}
		// Checagem de preenchimento desumano de formulário (< 800ms)
		nameLower := strings.ToLower(eventName)
		if (nameLower == "lead" || nameLower == "form_submit" || nameLower == "purchase") &&
			signals.TimeOnPageMs > 0 && signals.TimeOnPageMs < 800 {
			return true, "fast_form_fill"
		}

		if signals.ScreenW == 0 && signals.ScreenH == 0 && signals.TimeOnPageMs > 0 {
			return true, "zero_screen_dimension"
		}
	}

	// 2. Verificação de assinaturas no User-Agent
	if reHeadless.MatchString(ua) {
		return true, "headless_automation"
	}
	if reSocialPreview.MatchString(ua) {
		return true, "social_preview"
	}
	if reSearchCrawler.MatchString(ua) {
		return true, "search_crawler"
	}
	if reSEOScraper.MatchString(ua) {
		return true, "seo_scraper"
	}
	if reHTTPTool.MatchString(ua) {
		return true, "http_tool"
	}

	return false, ""
}
