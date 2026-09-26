-- 003_replacing_merge_tree.sql: Garante que a tabela use ReplacingMergeTree e ordene por event_id
-- Em clusters já iniciados, se a tabela já existir como ReplacingMergeTree este script é no-op seguro.

CREATE TABLE IF NOT EXISTS tracking_events.events_v2 (
    event_id UUID,
    site_id UUID,
    visitor_id UUID,
    session_id UUID,
    event_name LowCardinality(String),
    event_time DateTime64(3, 'UTC'),
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
    ip_address String,
    user_agent String,
    device_type LowCardinality(String),
    custom_data_json String,
    is_bot UInt8 DEFAULT 0,
    bot_reason LowCardinality(String) DEFAULT '',
    created_at DateTime DEFAULT now()
) ENGINE = ReplacingMergeTree(created_at)
PARTITION BY toYYYYMM(event_time)
ORDER BY (site_id, event_name, event_id, event_time, visitor_id)
SETTINGS index_granularity = 8192;
