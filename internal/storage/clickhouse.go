package storage

import (
	"context"
	"fmt"
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
	PageURL     string    `json:"page_url"`
	Pageviews   uint64    `json:"pageviews"`
	Visitors    uint64    `json:"visitors"`
	Conversions uint64    `json:"conversions"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

type LeadRecord struct {
	EventID        string    `json:"event_id"`
	SiteID         string    `json:"site_id"`
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

func (c *ClickHouseDB) GetAnalyticsOverview(ctx context.Context, siteID, rangeStr string) (*OverviewStats, error) {
	if c.Conn == nil {
		return nil, fmt.Errorf("clickhouse indisponivel")
	}

	since := ParseDateRange(rangeStr)
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
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') AND event_time >= ? AND is_bot = 0
	`
	row := c.Conn.QueryRow(ctx, queryKPIs, siteID, siteID, since)
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
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') AND event_time >= ? AND is_bot = 0
		GROUP BY dt
		ORDER BY dt ASC
	`
	rowsTS, err := c.Conn.Query(ctx, queryTimeSeries, siteID, siteID, since)
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
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') AND event_time >= ? AND is_bot = 0
		GROUP BY src, med, camp
		ORDER BY total DESC
		LIMIT 10
	`
	rowsSrc, err := c.Conn.Query(ctx, querySources, siteID, siteID, since)
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
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') AND event_time >= ? AND is_bot = 0
		GROUP BY dev
	`
	rowsDev, err := c.Conn.Query(ctx, queryDevices, siteID, siteID, since)
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

func (c *ClickHouseDB) GetMonitoredPages(ctx context.Context, siteID, rangeStr string) ([]PageItem, error) {
	if c.Conn == nil {
		return nil, fmt.Errorf("clickhouse indisponivel")
	}

	since := ParseDateRange(rangeStr)
	query := `
		SELECT 
			if(page_url != '', page_url, landing_page) AS url,
			countIf(event_name = 'page_view') AS pvs,
			uniqExact(visitor_id) AS visitors,
			countIf(event_name IN ('lead', 'whatsapp_click', 'purchase')) AS convs,
			max(event_time) AS last_seen
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') AND event_time >= ? AND is_bot = 0
		GROUP BY url
		ORDER BY pvs DESC
		LIMIT 100
	`
	rows, err := c.Conn.Query(ctx, query, siteID, siteID, since)
	if err != nil {
		return nil, fmt.Errorf("falha ao consultar paginas: %w", err)
	}
	defer rows.Close()

	pages := make([]PageItem, 0)
	for rows.Next() {
		var p PageItem
		if err := rows.Scan(&p.PageURL, &p.Pageviews, &p.Visitors, &p.Conversions, &p.LastSeenAt); err == nil {
			if p.PageURL != "" {
				pages = append(pages, p)
			}
		}
	}
	return pages, nil
}

func (c *ClickHouseDB) GetLeadsReport(ctx context.Context, siteID, rangeStr string, limit int) ([]LeadRecord, error) {
	if c.Conn == nil {
		return nil, fmt.Errorf("clickhouse indisponivel")
	}

	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	since := ParseDateRange(rangeStr)
	query := `
		SELECT 
			toString(event_id), toString(site_id), event_name, event_time, 
			page_url, landing_page, 
			utm_source, utm_medium, utm_campaign, 
			gclid, fbclid, device_type, ip_address, custom_data_json
		FROM tracking_events.events
		WHERE (site_id = toUUIDOrZero(?) OR ? = '') 
		  AND event_name IN ('lead', 'whatsapp_click', 'purchase') 
		  AND event_time >= ? 
		  AND is_bot = 0
		ORDER BY event_time DESC
		LIMIT ?
	`
	rows, err := c.Conn.Query(ctx, query, siteID, siteID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("falha ao consultar leads: %w", err)
	}
	defer rows.Close()

	leads := make([]LeadRecord, 0)
	for rows.Next() {
		var l LeadRecord
		if err := rows.Scan(
			&l.EventID, &l.SiteID, &l.EventName, &l.EventTime,
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
