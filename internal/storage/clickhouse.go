package storage

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"tracking-engine/internal/config"
)

type ClickHouseDB struct {
	Conn driver.Conn
}

func NewClickHouse(cfg *config.Config) (*ClickHouseDB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{cfg.ClickHouseAddr},
		Auth: clickhouse.Auth{
			Database: cfg.ClickHouseDB,
			Username: cfg.ClickHouseUser,
			Password: cfg.ClickHousePassword,
		},
		Settings: clickhouse.Settings{
			"max_execution_time": 60,
		},
		DialTimeout: 5 * time.Second,
		Compression: &clickhouse.Compression{
			Method: clickhouse.CompressionLZ4,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("erro ao abrir conexao com clickhouse: %w", err)
	}

	if err := conn.Ping(ctx); err != nil {
		// Loga aviso em vez de travar caso o container ainda esteja iniciando
		fmt.Printf("Aviso: ping no ClickHouse falhou (%v). O serviço continuará tentando.\n", err)
	}

	return &ClickHouseDB{Conn: conn}, nil
}

func (c *ClickHouseDB) Close() error {
	if c.Conn != nil {
		return c.Conn.Close()
	}
	return nil
}

type TimePoint struct {
	Date        string `json:"date"`
	Pageviews   uint64 `json:"pageviews"`
	Visitors    uint64 `json:"visitors"`
	Conversions uint64 `json:"conversions"`
}

type SourceItem struct {
	Source   string `json:"source"`
	Medium   string `json:"medium"`
	Campaign string `json:"campaign"`
	Count    uint64 `json:"count"`
}

type OverviewStats struct {
	TotalEvents         uint64            `json:"total_events"`
	UniqueVisitors      uint64            `json:"unique_visitors"`
	TotalLeads          uint64            `json:"total_leads"`
	TotalWhatsAppClicks uint64            `json:"total_whatsapp_clicks"`
	ActiveLast30Min     uint64            `json:"active_last_30_min"`
	TimeSeries          []TimePoint       `json:"time_series"`
	TopSources          []SourceItem      `json:"top_sources"`
	DeviceBreakdown     map[string]uint64 `json:"device_breakdown"`
}

type PageItem struct {
	PageTitle      string    `json:"page_title"`
	PageURL        string    `json:"page_url"`
	Pageviews      uint64    `json:"pageviews"`
	Visitors       uint64    `json:"visitors"`
	Conversions    uint64    `json:"conversions"`
	ConversionRate float64   `json:"conversion_rate"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

type MonitoredPagesTimePoint struct {
	Date        string `json:"date"`
	ActivePages uint64 `json:"active_pages"`
	Pageviews   uint64 `json:"pageviews"`
	Visitors    uint64 `json:"visitors"`
	Conversions uint64 `json:"conversions"`
}

type MonitoredPagesSummary struct {
	TotalActivePages  uint64  `json:"total_active_pages"`
	TotalPageviews    uint64  `json:"total_pageviews"`
	TotalVisitors     uint64  `json:"total_visitors"`
	TotalConversions  uint64  `json:"total_conversions"`
	AvgConversionRate float64 `json:"avg_conversion_rate"`
}

type MonitoredPagesReport struct {
	Summary    MonitoredPagesSummary     `json:"summary"`
	TimeSeries []MonitoredPagesTimePoint `json:"time_series"`
	Pages      []PageItem                `json:"pages"`
}

type LeadRecord struct {
	EventID        string    `json:"event_id"`
	SiteID         string    `json:"site_id"`
	VisitorID      string    `json:"visitor_id"`
	EventName      string    `json:"event_name"`
	EventTime      time.Time `json:"event_time"`
	PageURL        string    `json:"page_url"`
	LandingPage    string    `json:"landing_page"`
	UTMSource      string    `json:"utm_source"`
	UTMMedium      string    `json:"utm_medium"`
	UTMCampaign    string    `json:"utm_campaign"`
	GCLID          string    `json:"gclid"`
	FBCLID         string    `json:"fbclid"`
	DeviceType     string    `json:"device_type"`
	IPAddress      string    `json:"ip_address"`
	CustomDataJSON string    `json:"custom_data_json"`
	MissingAlerts  []string  `json:"missing_alerts"`
	IntegrityLevel string    `json:"integrity_level"`
}

type FunnelStep struct {
	StepNumber uint8   `json:"step_number"`
	Name       string  `json:"name"`
	Users      uint64  `json:"users"`
	RateTotal  float64 `json:"rate_total"`
	DropOff    float64 `json:"drop_off"`
}

type FunnelAnalysis struct {
	TotalVisitors uint64       `json:"total_visitors"`
	Steps         []FunnelStep `json:"steps"`
	Bottleneck    string       `json:"bottleneck"`
}

type AttributionPathItem struct {
	Path        string  `json:"path"`
	Conversions uint64  `json:"conversions"`
	AvgDays     float64 `json:"avg_days"`
	Percentage  float64 `json:"percentage"`
}

type ChannelAttributionComparison struct {
	Channel         string  `json:"channel"`
	FirstTouchCount uint64  `json:"first_touch_count"`
	FirstTouchShare float64 `json:"first_touch_share"`
	LastTouchCount  uint64  `json:"last_touch_count"`
	LastTouchShare  float64 `json:"last_touch_share"`
}

type AttributionPathsReport struct {
	TotalConversions uint64                         `json:"total_conversions"`
	TopPaths         []AttributionPathItem          `json:"top_paths"`
	ChannelCompare   []ChannelAttributionComparison `json:"channel_compare"`
}

type JourneyEventItem struct {
	EventID        string    `json:"event_id"`
	EventName      string    `json:"event_name"`
	EventTime      time.Time `json:"event_time"`
	PageURL        string    `json:"page_url"`
	LandingPage    string    `json:"landing_page"`
	Referrer       string    `json:"referrer"`
	UTMSource      string    `json:"utm_source"`
	UTMMedium      string    `json:"utm_medium"`
	UTMCampaign    string    `json:"utm_campaign"`
	GCLID          string    `json:"gclid"`
	FBCLID         string    `json:"fbclid"`
	DeviceType     string    `json:"device_type"`
	CustomDataJSON string    `json:"custom_data_json"`
}

type VisitorJourneyReport struct {
	VisitorID      string             `json:"visitor_id"`
	SiteID         string             `json:"site_id"`
	TotalEvents    int                `json:"total_events"`
	FirstSeen      time.Time          `json:"first_seen"`
	LastSeen       time.Time          `json:"last_seen"`
	IsConverted    bool               `json:"is_converted"`
	ConversionName string             `json:"conversion_name"`
	FirstTouch     string             `json:"first_touch"`
	LastTouch      string             `json:"last_touch"`
	Events         []JourneyEventItem `json:"events"`
}

func ParseDateRange(rangeStr string) time.Time {
	now := time.Now().UTC()
	switch rangeStr {
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	case "30d":
		return now.AddDate(0, 0, -30)
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "7d":
		fallthrough
	default:
		return now.AddDate(0, 0, -7)
	}
}

func (c *ClickHouseDB) GetAnalyticsOverview(ctx context.Context, siteID, domainFilter, rangeStr string) (*OverviewStats, error) {
	if c.Conn == nil {
		return nil, fmt.Errorf("clickhouse indisponivel")
	}

	since := ParseDateRange(rangeStr)
	domainFilter = strings.TrimSpace(domainFilter)
	stats := &OverviewStats{
		TimeSeries:      make([]TimePoint, 0),
		TopSources:      make([]SourceItem, 0),
		DeviceBreakdown: make(map[string]uint64),
	}

	// 1. KPIs Gerais
	queryKPIs := `
		SELECT 
			count(*) AS total_events,
			uniqExact(visitor_id) AS unique_visitors,
			countIf(event_name = 'lead') AS total_leads,
			countIf(event_name = 'whatsapp_click') AS total_whatsapp,
			uniqExactIf(visitor_id, event_time >= now() - INTERVAL 30 MINUTE) AS active_30m
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_time >= ? AND is_bot = 0
	`
	row := c.Conn.QueryRow(ctx, queryKPIs, siteID, siteID, domainFilter, domainFilter, since)
	if err := row.Scan(&stats.TotalEvents, &stats.UniqueVisitors, &stats.TotalLeads, &stats.TotalWhatsAppClicks, &stats.ActiveLast30Min); err != nil {
		return nil, fmt.Errorf("falha ao consultar KPIs gerais: %w", err)
	}

	// 2. Série Temporal (Por Dia)
	queryTimeSeries := `
		SELECT 
			toStartOfInterval(event_time, INTERVAL 1 DAY) AS dt,
			countIf(event_name = 'page_view') AS pvs,
			uniqExact(visitor_id) AS visitors,
			countIf(event_name IN ('lead', 'whatsapp_click', 'purchase')) AS convs
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_time >= ? AND is_bot = 0
		GROUP BY dt
		ORDER BY dt ASC
	`
	rowsTS, err := c.Conn.Query(ctx, queryTimeSeries, siteID, siteID, domainFilter, domainFilter, since)
	if err == nil {
		defer rowsTS.Close()
		for rowsTS.Next() {
			var dt time.Time
			var pvs, visitors, convs uint64
			if err := rowsTS.Scan(&dt, &pvs, &visitors, &convs); err == nil {
				stats.TimeSeries = append(stats.TimeSeries, TimePoint{
					Date:        dt.Format("02/01"),
					Pageviews:   pvs,
					Visitors:    visitors,
					Conversions: convs,
				})
			}
		}
	}

	// 3. Top Sources / Campanhas
	querySources := `
		SELECT 
			if(utm_source = '', '(direto)', utm_source) AS src,
			if(utm_medium = '', '(nenhum)', utm_medium) AS med,
			if(utm_campaign = '', '(sem campanha)', utm_campaign) AS camp,
			count(*) AS total
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_time >= ? AND is_bot = 0
		GROUP BY src, med, camp
		ORDER BY total DESC
		LIMIT 10
	`
	rowsSrc, err := c.Conn.Query(ctx, querySources, siteID, siteID, domainFilter, domainFilter, since)
	if err == nil {
		defer rowsSrc.Close()
		for rowsSrc.Next() {
			var src, med, camp string
			var total uint64
			if err := rowsSrc.Scan(&src, &med, &camp, &total); err == nil {
				stats.TopSources = append(stats.TopSources, SourceItem{
					Source:   src,
					Medium:   med,
					Campaign: camp,
					Count:    total,
				})
			}
		}
	}

	// 4. Dispositivos
	queryDevices := `
		SELECT 
			if(device_type = '', 'desktop', device_type) AS dev,
			count(*) AS total
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_time >= ? AND is_bot = 0
		GROUP BY dev
	`
	rowsDev, err := c.Conn.Query(ctx, queryDevices, siteID, siteID, domainFilter, domainFilter, since)
	if err == nil {
		defer rowsDev.Close()
		for rowsDev.Next() {
			var dev string
			var total uint64
			if err := rowsDev.Scan(&dev, &total); err == nil {
				stats.DeviceBreakdown[dev] = total
			}
		}
	}

	return stats, nil
}

func (c *ClickHouseDB) GetMonitoredPagesReport(ctx context.Context, siteID, domainFilter, rangeStr string) (*MonitoredPagesReport, error) {
	if c.Conn == nil {
		return nil, fmt.Errorf("clickhouse indisponivel")
	}

	since := ParseDateRange(rangeStr)
	domainFilter = strings.TrimSpace(domainFilter)
	report := &MonitoredPagesReport{
		Summary:    MonitoredPagesSummary{},
		TimeSeries: make([]MonitoredPagesTimePoint, 0),
		Pages:      make([]PageItem, 0),
	}

	// 1. Série Temporal Diária de Páginas Ativas & Volume
	queryTS := `
		SELECT 
			toStartOfInterval(event_time, INTERVAL 1 DAY) AS dt,
			uniqExact(splitByChar('#', cutQueryString(if(page_url != '', page_url, landing_page)))[1]) AS active_pages,
			countIf(event_name = 'page_view') AS pvs,
			uniqExact(visitor_id) AS visitors,
			countIf(event_name IN ('lead', 'whatsapp_click', 'purchase')) AS convs
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_time >= ? AND is_bot = 0
		GROUP BY dt
		ORDER BY dt ASC
	`
	rowsTS, err := c.Conn.Query(ctx, queryTS, siteID, siteID, domainFilter, domainFilter, since)
	if err == nil {
		defer rowsTS.Close()
		for rowsTS.Next() {
			var dt time.Time
			var activePages, pvs, visitors, convs uint64
			if err := rowsTS.Scan(&dt, &activePages, &pvs, &visitors, &convs); err == nil {
				report.TimeSeries = append(report.TimeSeries, MonitoredPagesTimePoint{
					Date:        dt.Format("02/01"),
					ActivePages: activePages,
					Pageviews:   pvs,
					Visitors:    visitors,
					Conversions: convs,
				})
			}
		}
	}

	// 2. Tabela de Páginas Consolidadas com Canonicalização e Extração de Título
	queryPages := `
		SELECT 
			splitByChar('#', cutQueryString(if(page_url != '', page_url, landing_page)))[1] AS clean_url,
			argMax(JSONExtractString(custom_data_json, 'page_title'), event_time) AS title,
			countIf(event_name = 'page_view') AS pvs,
			uniqExact(visitor_id) AS visitors,
			countIf(event_name IN ('lead', 'whatsapp_click', 'purchase')) AS convs,
			max(event_time) AS last_seen
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_time >= ? AND is_bot = 0
		GROUP BY clean_url
		HAVING clean_url != ''
		ORDER BY pvs DESC
		LIMIT 200
	`
	rows, err := c.Conn.Query(ctx, queryPages, siteID, siteID, domainFilter, domainFilter, since)
	if err != nil {
		return nil, fmt.Errorf("falha ao consultar paginas: %w", err)
	}
	defer rows.Close()

	var totalPVs, totalConvs uint64
	for rows.Next() {
		var p PageItem
		var rawTitle string
		if err := rows.Scan(&p.PageURL, &rawTitle, &p.Pageviews, &p.Visitors, &p.Conversions, &p.LastSeenAt); err == nil {
			p.PageTitle = strings.TrimSpace(rawTitle)
			if p.PageURL != "" {
				if p.Visitors > 0 {
					p.ConversionRate = math.Round((float64(p.Conversions)/float64(p.Visitors)*100)*100) / 100
				}
				totalPVs += p.Pageviews
				totalConvs += p.Conversions
				report.Pages = append(report.Pages, p)
			}
		}
	}

	report.Summary.TotalActivePages = uint64(len(report.Pages))
	report.Summary.TotalPageviews = totalPVs
	report.Summary.TotalConversions = totalConvs

	// Consulta de visitantes únicos totais no período para o resumo
	queryVisitors := `
		SELECT uniqExact(visitor_id)
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_time >= ? AND is_bot = 0
	`
	rowVis := c.Conn.QueryRow(ctx, queryVisitors, siteID, siteID, domainFilter, domainFilter, since)
	_ = rowVis.Scan(&report.Summary.TotalVisitors)

	if report.Summary.TotalVisitors > 0 {
		report.Summary.AvgConversionRate = math.Round((float64(report.Summary.TotalConversions)/float64(report.Summary.TotalVisitors)*100)*100) / 100
	}

	return report, nil
}

func (c *ClickHouseDB) GetMonitoredPages(ctx context.Context, siteID, domainFilter, rangeStr string) ([]PageItem, error) {
	rep, err := c.GetMonitoredPagesReport(ctx, siteID, domainFilter, rangeStr)
	if err != nil {
		return nil, err
	}
	return rep.Pages, nil
}

func (c *ClickHouseDB) GetLeadsReport(ctx context.Context, siteID, domainFilter, rangeStr string, limit int) ([]LeadRecord, error) {
	if c.Conn == nil {
		return nil, fmt.Errorf("clickhouse indisponivel")
	}

	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	since := ParseDateRange(rangeStr)
	domainFilter = strings.TrimSpace(domainFilter)
	query := `
		SELECT 
			toString(event_id), toString(site_id), toString(visitor_id), event_name, event_time, 
			page_url, landing_page, 
			utm_source, utm_medium, utm_campaign, 
			gclid, fbclid, device_type, ip_address, custom_data_json
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_name IN ('lead', 'whatsapp_click', 'purchase') 
		  AND event_time >= ? 
		  AND is_bot = 0
		ORDER BY event_time DESC
		LIMIT ?
	`
	rows, err := c.Conn.Query(ctx, query, siteID, siteID, domainFilter, domainFilter, since, limit)
	if err != nil {
		return nil, fmt.Errorf("falha ao consultar leads: %w", err)
	}
	defer rows.Close()

	leads := make([]LeadRecord, 0)
	for rows.Next() {
		var l LeadRecord
		if err := rows.Scan(
			&l.EventID, &l.SiteID, &l.VisitorID, &l.EventName, &l.EventTime,
			&l.PageURL, &l.LandingPage,
			&l.UTMSource, &l.UTMMedium, &l.UTMCampaign,
			&l.GCLID, &l.FBCLID, &l.DeviceType, &l.IPAddress, &l.CustomDataJSON,
		); err == nil {
			// Auditoria de integridade de mídia paga
			l.MissingAlerts = make([]string, 0)
			hasUTM := l.UTMSource != "" || l.UTMCampaign != ""
			hasClickID := l.GCLID != "" || l.FBCLID != ""

			if !hasUTM {
				l.MissingAlerts = append(l.MissingAlerts, "Sem Campanha/UTMs")
			}
			if !hasClickID {
				l.MissingAlerts = append(l.MissingAlerts, "Sem Click ID Ads")
			}
			if l.PageURL == "" && l.LandingPage == "" {
				l.MissingAlerts = append(l.MissingAlerts, "Sem URL de Origem")
			}

			if len(l.MissingAlerts) == 0 {
				l.IntegrityLevel = "ok"
			} else if !hasUTM && !hasClickID {
				l.IntegrityLevel = "warning_high"
			} else {
				l.IntegrityLevel = "warning_medium"
			}

			leads = append(leads, l)
		}
	}
	return leads, nil
}

// GetFunnelAnalysis calcula a retenção e taxas de abandono (drop-off) em 4 etapas do funil
func (c *ClickHouseDB) GetFunnelAnalysis(ctx context.Context, siteID, domainFilter, rangeStr string) (*FunnelAnalysis, error) {
	if c.Conn == nil {
		return nil, fmt.Errorf("clickhouse indisponivel")
	}

	since := ParseDateRange(rangeStr)
	domainFilter = strings.TrimSpace(domainFilter)
	query := `
		SELECT 
			uniqExact(visitor_id) AS total_visitors,
			uniqExactIf(visitor_id, event_name = 'page_view') AS step1_views,
			uniqExactIf(visitor_id, event_name NOT IN ('page_view', 'bot_detected') OR visitor_id IN (
				SELECT visitor_id FROM tracking_events.events 
				WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
				  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
				  AND event_time >= ? AND is_bot = 0 
				GROUP BY visitor_id HAVING count(*) >= 2
			)) AS step2_engaged,
			uniqExactIf(visitor_id, event_name IN ('whatsapp_click', 'cta_click', 'click', 'form_start')) AS step3_cta,
			uniqExactIf(visitor_id, event_name IN ('lead', 'purchase', 'form_submit', 'contact')) AS step4_conv
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_time >= ? AND is_bot = 0
	`
	var totalVisitors, step1, step2, step3, step4 uint64
	row := c.Conn.QueryRow(ctx, query, siteID, siteID, domainFilter, domainFilter, since, siteID, siteID, domainFilter, domainFilter, since)
	if err := row.Scan(&totalVisitors, &step1, &step2, &step3, &step4); err != nil {
		return nil, fmt.Errorf("falha ao consultar funil: %w", err)
	}

	if step1 == 0 {
		step1 = totalVisitors
	}
	if step2 > step1 {
		step2 = step1
	}
	if step3 > step2 {
		step3 = step2
	}
	if step4 > step3 {
		step4 = step3
	}

	calcRate := func(val, base uint64) float64 {
		if base == 0 {
			return 0.0
		}
		return math.Round((float64(val)/float64(base))*1000) / 10.0
	}

	calcDropOff := func(current, prev uint64) float64 {
		if prev == 0 || current >= prev {
			return 0.0
		}
		lost := prev - current
		return math.Round((float64(lost)/float64(prev))*1000) / 10.0
	}

	steps := []FunnelStep{
		{
			StepNumber: 1,
			Name:       "Visualização da Página",
			Users:      step1,
			RateTotal:  100.0,
			DropOff:    0.0,
		},
		{
			StepNumber: 2,
			Name:       "Engajamento / Navegação",
			Users:      step2,
			RateTotal:  calcRate(step2, step1),
			DropOff:    calcDropOff(step2, step1),
		},
		{
			StepNumber: 3,
			Name:       "Clique em CTA / WhatsApp",
			Users:      step3,
			RateTotal:  calcRate(step3, step1),
			DropOff:    calcDropOff(step3, step2),
		},
		{
			StepNumber: 4,
			Name:       "Conversão Final Concluída",
			Users:      step4,
			RateTotal:  calcRate(step4, step1),
			DropOff:    calcDropOff(step4, step3),
		},
	}

	bottleneck := "Fluxo com conversão contínua"
	maxDrop := 0.0
	for i := 1; i < len(steps); i++ {
		if steps[i].DropOff > maxDrop && steps[i].DropOff > 0 {
			maxDrop = steps[i].DropOff
			bottleneck = fmt.Sprintf("Gargalo entre Etapa %d (%s) e Etapa %d (%s): %.1f%% de abandono",
				steps[i-1].StepNumber, steps[i-1].Name, steps[i].StepNumber, steps[i].Name, maxDrop)
		}
	}

	return &FunnelAnalysis{
		TotalVisitors: totalVisitors,
		Steps:         steps,
		Bottleneck:    bottleneck,
	}, nil
}

// GetAttributionPaths agrega sequências de canais e calcula comparativo First-Touch vs Last-Touch
func (c *ClickHouseDB) GetAttributionPaths(ctx context.Context, siteID, domainFilter, rangeStr string, limit int) (*AttributionPathsReport, error) {
	if c.Conn == nil {
		return nil, fmt.Errorf("clickhouse indisponivel")
	}

	if limit <= 0 || limit > 50 {
		limit = 15
	}
	since := ParseDateRange(rangeStr)
	domainFilter = strings.TrimSpace(domainFilter)

	queryTotalConvs := `
		SELECT uniqExact(visitor_id)
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
		  AND event_time >= ? AND is_bot = 0
		  AND event_name IN ('lead', 'purchase', 'whatsapp_click')
	`
	var totalConvs uint64
	_ = c.Conn.QueryRow(ctx, queryTotalConvs, siteID, siteID, domainFilter, domainFilter, since).Scan(&totalConvs)

	report := &AttributionPathsReport{
		TotalConversions: totalConvs,
		TopPaths:         make([]AttributionPathItem, 0),
		ChannelCompare:   make([]ChannelAttributionComparison, 0),
	}

	queryPaths := `
		SELECT 
			path,
			count(*) AS conversions,
			round(avg(days_to_convert), 1) AS avg_days
		FROM (
			SELECT 
				visitor_id,
				arrayStringConcat(groupArray(src), ' ➔ ') AS path,
				dateDiff('hour', min(first_seen), max(conv_time)) / 24.0 AS days_to_convert
			FROM (
				SELECT 
					visitor_id,
					if(utm_source != '', utm_source, '(direto)') AS src,
					min(event_time) AS first_seen,
					max(event_time) AS conv_time
				FROM tracking_events.events
				WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
				  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
				  AND event_time >= ? AND is_bot = 0
				GROUP BY visitor_id, src
				ORDER BY min(event_time) ASC
			)
			WHERE visitor_id IN (
				SELECT DISTINCT visitor_id 
				FROM tracking_events.events 
				WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
				  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
				  AND event_time >= ? AND is_bot = 0
				  AND event_name IN ('lead', 'purchase', 'whatsapp_click')
			)
			GROUP BY visitor_id
		)
		WHERE path != ''
		GROUP BY path
		ORDER BY conversions DESC
		LIMIT ?
	`
	rows, err := c.Conn.Query(ctx, queryPaths, siteID, siteID, domainFilter, domainFilter, since, siteID, siteID, domainFilter, domainFilter, since, limit)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var item AttributionPathItem
			if err := rows.Scan(&item.Path, &item.Conversions, &item.AvgDays); err == nil {
				if totalConvs > 0 {
					item.Percentage = math.Round((float64(item.Conversions)/float64(totalConvs))*1000) / 10.0
				}
				report.TopPaths = append(report.TopPaths, item)
			}
		}
	}

	firstMap := make(map[string]uint64)
	queryFirst := `
		SELECT 
			if(utm_source != '', utm_source, '(direto)') AS chan,
			count(DISTINCT visitor_id) AS total
		FROM (
			SELECT visitor_id, utm_source, row_number() OVER (PARTITION BY visitor_id ORDER BY event_time ASC) as rn
			FROM tracking_events.events
			WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
			  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
			  AND event_time >= ? AND is_bot = 0
			  AND visitor_id IN (
				SELECT DISTINCT visitor_id FROM tracking_events.events 
				WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
				  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
				  AND event_time >= ? AND is_bot = 0
				  AND event_name IN ('lead', 'purchase', 'whatsapp_click')
			  )
		)
		WHERE rn = 1
		GROUP BY chan
	`
	rowsFirst, errF := c.Conn.Query(ctx, queryFirst, siteID, siteID, domainFilter, domainFilter, since, siteID, siteID, domainFilter, domainFilter, since)
	if errF == nil {
		defer rowsFirst.Close()
		for rowsFirst.Next() {
			var ch string
			var cnt uint64
			if err := rowsFirst.Scan(&ch, &cnt); err == nil {
				firstMap[ch] = cnt
			}
		}
	}

	lastMap := make(map[string]uint64)
	queryLast := `
		SELECT 
			if(utm_source != '', utm_source, '(direto)') AS chan,
			count(DISTINCT visitor_id) AS total
		FROM (
			SELECT visitor_id, utm_source, row_number() OVER (PARTITION BY visitor_id ORDER BY event_time DESC) as rn
			FROM tracking_events.events
			WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
			  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
			  AND event_time >= ? AND is_bot = 0
			  AND visitor_id IN (
				SELECT DISTINCT visitor_id FROM tracking_events.events 
				WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
				  AND (domainWithoutWWW(if(page_url != '', page_url, landing_page)) = domainWithoutWWW(?) OR ? = '')
				  AND event_time >= ? AND is_bot = 0
				  AND event_name IN ('lead', 'purchase', 'whatsapp_click')
			  )
		)
		WHERE rn = 1
		GROUP BY chan
	`
	rowsLast, errL := c.Conn.Query(ctx, queryLast, siteID, siteID, domainFilter, domainFilter, since, siteID, siteID, domainFilter, domainFilter, since)
	if errL == nil {
		defer rowsLast.Close()
		for rowsLast.Next() {
			var ch string
			var cnt uint64
			if err := rowsLast.Scan(&ch, &cnt); err == nil {
				lastMap[ch] = cnt
			}
		}
	}

	allChannels := make(map[string]bool)
	for ch := range firstMap {
		allChannels[ch] = true
	}
	for ch := range lastMap {
		allChannels[ch] = true
	}

	for ch := range allChannels {
		fCount := firstMap[ch]
		lCount := lastMap[ch]
		comp := ChannelAttributionComparison{
			Channel:         ch,
			FirstTouchCount: fCount,
			LastTouchCount:  lCount,
		}
		if totalConvs > 0 {
			comp.FirstTouchShare = math.Round((float64(fCount)/float64(totalConvs))*1000) / 10.0
			comp.LastTouchShare = math.Round((float64(lCount)/float64(totalConvs))*1000) / 10.0
		}
		report.ChannelCompare = append(report.ChannelCompare, comp)
	}

	return report, nil
}

// GetVisitorJourney recupera a fita do tempo cronológica com todos os eventos de um visitante
func (c *ClickHouseDB) GetVisitorJourney(ctx context.Context, visitorID, siteID string) (*VisitorJourneyReport, error) {
	if c.Conn == nil {
		return nil, fmt.Errorf("clickhouse indisponivel")
	}

	cleanVisitorID := strings.TrimPrefix(strings.TrimSpace(visitorID), "v_")
	if cleanVisitorID == "" {
		return nil, fmt.Errorf("visitor_id e obrigatorio")
	}

	query := `
		SELECT 
			toString(event_id), toString(site_id),
			event_name, event_time, 
			page_url, landing_page, referrer,
			utm_source, utm_medium, utm_campaign, 
			gclid, fbclid, device_type, custom_data_json
		FROM tracking_events.events
		WHERE (visitor_id = toUUIDOrZero(?) OR toString(visitor_id) = ?)
		  AND (site_id = toUUIDOrZero(?) OR ? = '')
		  AND is_bot = 0
		ORDER BY event_time ASC
		LIMIT 200
	`
	rows, err := c.Conn.Query(ctx, query, cleanVisitorID, cleanVisitorID, siteID, siteID)
	if err != nil {
		return nil, fmt.Errorf("falha ao consultar jornada do visitante: %w", err)
	}
	defer rows.Close()

	report := &VisitorJourneyReport{
		VisitorID: visitorID,
		SiteID:    siteID,
		Events:    make([]JourneyEventItem, 0),
	}

	for rows.Next() {
		var item JourneyEventItem
		var evSiteID string
		if err := rows.Scan(
			&item.EventID, &evSiteID,
			&item.EventName, &item.EventTime,
			&item.PageURL, &item.LandingPage, &item.Referrer,
			&item.UTMSource, &item.UTMMedium, &item.UTMCampaign,
			&item.GCLID, &item.FBCLID, &item.DeviceType, &item.CustomDataJSON,
		); err == nil {
			if report.SiteID == "" && evSiteID != "" {
				report.SiteID = evSiteID
			}
			if report.FirstSeen.IsZero() {
				report.FirstSeen = item.EventTime
			}
			report.LastSeen = item.EventTime

			name := strings.ToLower(item.EventName)
			if name == "lead" || name == "purchase" || name == "whatsapp_click" || name == "contact" {
				report.IsConverted = true
				report.ConversionName = item.EventName
			}

			src := item.UTMSource
			if src == "" {
				src = "(direto)"
			}
			if report.FirstTouch == "" {
				report.FirstTouch = src
			}
			report.LastTouch = src

			report.Events = append(report.Events, item)
		}
	}
	report.TotalEvents = len(report.Events)
	return report, nil
}

