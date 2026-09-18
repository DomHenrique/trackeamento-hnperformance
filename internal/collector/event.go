package collector

import (
	"time"

	"tracking-engine/internal/attribution"
)

type EventRequest struct {
	SiteKey    string                 `json:"site_key"`
	EventName  string                 `json:"event_name"`
	EventID    string                 `json:"event_id,omitempty"` // Se passado pelo frontend para deduplicação com Meta Pixel
	SessionID  string                 `json:"session_id,omitempty"`
	URL        string                 `json:"url,omitempty"`
	PageURL    string                 `json:"page_url,omitempty"`
	Referrer   string                 `json:"referrer,omitempty"`
	UserData   map[string]interface{} `json:"user_data,omitempty"`   // email, phone, name
	CustomData map[string]interface{} `json:"custom_data,omitempty"` // value, currency, etc.
}

type EventPayload struct {
	EventID     string                 `json:"event_id"`
	SiteID      string                 `json:"site_id"`
	SiteKey     string                 `json:"site_key"`
	VisitorID   string                 `json:"visitor_id"`
	SessionID   string                 `json:"session_id"`
	EventName   string                 `json:"event_name"`
	EventTime   time.Time              `json:"event_time"`
	IPAddress   string                 `json:"ip_address"`
	UserAgent   string                 `json:"user_agent"`
	DeviceType  string                 `json:"device_type"`
	Attribution attribution.Params     `json:"attribution"`
	UserData    map[string]interface{} `json:"user_data,omitempty"`
	CustomData  map[string]interface{} `json:"custom_data,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
}
