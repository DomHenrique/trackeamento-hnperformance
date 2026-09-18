CREATE DATABASE IF NOT EXISTS tracking_events;

CREATE TABLE IF NOT EXISTS tracking_events.events (
    event_id UUID,
    site_id UUID,
    visitor_id UUID,
    session_id UUID,
    event_name LowCardinality(String),
    event_time DateTime64(3, 'UTC'),
    
    -- URLs e Atribuição
    landing_page String,
    page_url String,
    referrer String,
    utm_source LowCardinality(String),
    utm_medium LowCardinality(String),
    utm_campaign String,
    utm_content String,
    utm_term String,
    gclid String,
    gbraid String,
    wbraid String,
    fbclid String,
    ttclid String,
    
    -- Metadados de Rede e Dispositivo
    ip_address String,
    user_agent String,
    device_type LowCardinality(String),
    
    -- Carga útil personalizada do evento (valores, moeda, etc.)
    custom_data_json String,
    created_at DateTime DEFAULT now()
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(event_time)
ORDER BY (site_id, event_name, event_time, visitor_id)
SETTINGS index_granularity = 8192;
