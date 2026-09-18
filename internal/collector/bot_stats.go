package collector

import (
	"context"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
)

type BotStatsResponse struct {
	TotalBots            int64            `json:"total_bots"`
	ProtectedConversions int64            `json:"protected_conversions"`
	Reasons              map[string]int64 `json:"reasons"`
	Source               string           `json:"source"`
}

// HandleGetBotStats retorna o agregado de eventos de robôs interceptados e conversões protegidas
func (h *Handler) HandleGetBotStats(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 4*time.Second)
	defer cancel()

	resp := BotStatsResponse{
		Reasons: make(map[string]int64),
		Source:  "none",
	}

	// 1. Tenta consulta ao ClickHouse para agregados precisos
	if h.ch != nil && h.ch.Conn != nil {
		var totalBots uint64
		var protectedConv uint64

		queryTotals := `
			SELECT 
				count() AS total_bots,
				countIf(event_name IN ('lead', 'purchase', 'whatsapp_click', 'form_submit', 'contact')) AS protected_conversions
			FROM tracking_events.events
			WHERE is_bot = 1
		`
		row := h.ch.Conn.QueryRow(ctx, queryTotals)
		if err := row.Scan(&totalBots, &protectedConv); err == nil {
			resp.TotalBots = int64(totalBots)
			resp.ProtectedConversions = int64(protectedConv)
			resp.Source = "clickhouse"

			queryReasons := `
				SELECT bot_reason, count() AS total
				FROM tracking_events.events
				WHERE is_bot = 1 AND bot_reason != ''
				GROUP BY bot_reason
				ORDER BY total DESC
				LIMIT 10
			`
			rows, errR := h.ch.Conn.Query(ctx, queryReasons)
			if errR == nil {
				defer rows.Close()
				for rows.Next() {
					var reason string
					var count uint64
					if err := rows.Scan(&reason, &count); err == nil {
						resp.Reasons[reason] = int64(count)
					}
				}
			}
			return c.Status(fiber.StatusOK).JSON(resp)
		}
	}

	// 2. Fallback para contadores em tempo real do Redis
	if h.redis != nil && h.redis.Client != nil {
		totStr, _ := h.redis.Client.Get(ctx, "stats:bots:total").Result()
		convStr, _ := h.redis.Client.Get(ctx, "stats:bots:protected_conversions").Result()
		reasonsMap, _ := h.redis.Client.HGetAll(ctx, "stats:bots:reasons").Result()

		if totStr != "" {
			resp.TotalBots, _ = strconv.ParseInt(totStr, 10, 64)
		}
		if convStr != "" {
			resp.ProtectedConversions, _ = strconv.ParseInt(convStr, 10, 64)
		}
		for r, v := range reasonsMap {
			cVal, _ := strconv.ParseInt(v, 10, 64)
			resp.Reasons[r] = cVal
		}
		resp.Source = "redis"
		return c.Status(fiber.StatusOK).JSON(resp)
	}

	return c.Status(fiber.StatusOK).JSON(resp)
}
