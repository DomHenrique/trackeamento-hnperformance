/**
 * HN Performance Server-Side Tracker SDK v1.0
 * Lightweight (< 4KB), zero-dependencies, 1st-party tracking & attribution.
 */
(function (window, document) {
    'use strict';

    // 1. Identificação do Script e Configurações
    var currentScript = document.currentScript || (function () {
        var scripts = document.getElementsByTagName('script');
        return scripts[scripts.length - 1];
    })();

    var rawSiteKey = (currentScript && currentScript.getAttribute('data-site-key')) || window.__HN_SITE_KEY__ || '';
    var siteKey = (rawSiteKey || '').trim().replace(/^["'`]|["'`]$/g, '');
    var apiEndpoint = (currentScript && currentScript.getAttribute('data-endpoint')) || 
                      (currentScript && currentScript.src ? currentScript.src.replace(/\/sdk\/tracker\.js.*$/, '/api/v1/collect') : '/api/v1/collect');

    // Validação defensiva do formato da chave para alertar desenvolvedores
    if (siteKey) {
        if (!siteKey.startsWith('hn_site_') && !siteKey.startsWith('hn_live_key_')) {
            console.warn('[HN Tracker] AVISO: A chave informada em data-site-key ("' + siteKey + '") não utiliza o padrão "hn_site_...". Verifique se você não colou um identificador de visitante (hn_vis_) ou outro parâmetro por engano.');
        }
    }

    // Detecção de Modo Debug (GTM Preview, URL params, sessionStorage, script attr ou global)
    var isDebug = false;
    try {
        var searchStr = window.location.search || '';
        var isGTMPreview = searchStr.indexOf('gtm_debug=') !== -1 || (document.referrer && document.referrer.indexOf('tagassistant.google.com') !== -1);
        var hasUrlDebug = searchStr.indexOf('hn_debug=true') !== -1 || searchStr.indexOf('hn_debug=1') !== -1 || searchStr.indexOf('debug_mode=1') !== -1 || searchStr.indexOf('debug_mode=true') !== -1;
        var hasSessionDebug = sessionStorage.getItem('_hn_debug') === '1';
        var hasScriptDebug = currentScript && (currentScript.getAttribute('data-debug') === 'true' || currentScript.getAttribute('data-debug') === '1');
        var hasGlobalDebug = window.__HN_DEBUG__ === true;

        if (isGTMPreview || hasUrlDebug || hasSessionDebug || hasScriptDebug || hasGlobalDebug) {
            isDebug = true;
            sessionStorage.setItem('_hn_debug', '1');
        }
    } catch (e) {}

    function debugLog() {
        if (isDebug && window.console && console.log) {
            var args = Array.prototype.slice.call(arguments);
            args.unshift('%c[HN Tracker DEBUG]', 'background: #6366f1; color: #fff; font-weight: bold; padding: 2px 6px; border-radius: 4px;');
            console.log.apply(console, args);
        }
    }

    if (isDebug) {
        debugLog('⚡ Modo Debug ativo! Eventos serão transmitidos ao vivo para o DebugView e isolados do ClickHouse.');
    }

    // 2. Gerador criptográfico de Type-Prefixed IDs
    function generatePrefixedId(prefix, len) {
        len = len || 24;
        var chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
        var result = '';
        if (window.crypto && window.crypto.getRandomValues) {
            var bytes = new Uint8Array(len);
            window.crypto.getRandomValues(bytes);
            for (var i = 0; i < len; i++) {
                result += chars[bytes[i] % chars.length];
            }
        } else {
            for (var j = 0; j < len; j++) {
                result += chars.charAt(Math.floor(Math.random() * chars.length));
            }
        }
        return prefix + result;
    }

    // 3. Gestão de Sessão (hn_ses_...)
    var SESSION_KEY = '_hn_sid';
    var sessionId = sessionStorage.getItem(SESSION_KEY);
    if (!sessionId) {
        sessionId = generatePrefixedId('hn_ses_', 24);
        sessionStorage.setItem(SESSION_KEY, sessionId);
    }

    // 4. Captura e Persistência de Atribuição (UTMs e Click IDs)
    var ATTR_STORAGE_KEY = '_hn_attr';
    var trackedParams = ['utm_source', 'utm_medium', 'utm_campaign', 'utm_content', 'utm_term', 'gclid', 'gbraid', 'wbraid', 'fbclid', 'ttclid'];

    function parseURLParams() {
        var params = {};
        try {
            var search = window.location.search.substring(1);
            if (search) {
                var pairs = search.split('&');
                for (var i = 0; i < pairs.length; i++) {
                    var pair = pairs[i].split('=');
                    var key = decodeURIComponent(pair[0]);
                    var val = decodeURIComponent(pair[1] || '');
                    if (trackedParams.indexOf(key) !== -1 && val) {
                        params[key] = val;
                    }
                }
            }
        } catch (e) {}
        return params;
    }

    // Salva ou recupera parâmetros anteriores (first-touch persistence no navegador)
    var currentParams = parseURLParams();
    var storedParams = {};
    try {
        var rawStored = localStorage.getItem(ATTR_STORAGE_KEY);
        if (rawStored) storedParams = JSON.parse(rawStored);
    } catch (e) {}

    // Mescla parâmetros atuais sobrescrevendo se novos forem encontrados
    for (var k in currentParams) {
        if (currentParams.hasOwnProperty(k)) storedParams[k] = currentParams[k];
    }
    try {
        localStorage.setItem(ATTR_STORAGE_KEY, JSON.stringify(storedParams));
    } catch (e) {}

    // Helper para extrair cookies de parceiros se existirem (_fbp, _fbc)
    function getCookie(name) {
        var match = document.cookie.match(new RegExp('(^|;\\s*)(' + name + ')=([^;]*)'));
        return match ? decodeURIComponent(match[3]) : '';
    }

    // Telemetria passiva de interação e detecção de automação
    var pageLoadTime = Date.now();
    var hasInteracted = false;
    var firstInteractionTime = 0;

    function onFirstInteraction() {
        if (!hasInteracted) {
            hasInteracted = true;
            firstInteractionTime = Date.now();
        }
    }
    if (window.addEventListener) {
        window.addEventListener('mousemove', onFirstInteraction, { once: true, passive: true });
        window.addEventListener('scroll', onFirstInteraction, { once: true, passive: true });
        window.addEventListener('touchstart', onFirstInteraction, { once: true, passive: true });
        window.addEventListener('keydown', onFirstInteraction, { once: true, passive: true });
    }

    function getClientSignals() {
        var now = Date.now();
        var nav = window.navigator || {};
        var scr = window.screen || {};
        var isWebdriver = !!(nav.webdriver);
        var isHeadless = !!(
            window._phantom ||
            window.callPhantom ||
            window.__puppeteer_evaluation_script__ ||
            window.__nightmare
        );

        return {
            webdriver: isWebdriver,
            headless: isHeadless,
            screen_w: scr.width || 0,
            screen_h: scr.height || 0,
            time_to_interact_ms: firstInteractionTime ? (firstInteractionTime - pageLoadTime) : 0,
            time_on_page_ms: Math.max(0, now - pageLoadTime)
        };
    }

    // 5. Função Principal de Disparo de Eventos
    function trackEvent(eventName, userData, customData) {
        if (!siteKey) {
            console.warn('[HN Tracker] data-site-key não configurada.');
            return;
        }

        var eventId = generatePrefixedId('hn_evt_', 24); // Gerado no cliente para deduplicação com Meta Pixel / Google CAPI
        userData = userData || {};
        customData = customData || {};

        // Injeta cookies Meta caso presentes
        var fbp = getCookie('_fbp');
        var fbc = getCookie('_fbc');
        if (fbp && !userData.fbp) userData.fbp = fbp;
        if (fbc && !userData.fbc) userData.fbc = fbc;

        var eventIsDebug = isDebug || (customData && (customData.debug === true || customData.debug_mode === true || customData.is_debug === true));

        var payload = {
            site_key: siteKey,
            event_name: eventName,
            event_id: eventId,
            session_id: sessionId,
            url: window.location.href,
            referrer: document.referrer || '',
            user_data: userData,
            custom_data: customData,
            client_signals: getClientSignals(),
            is_debug: !!eventIsDebug
        };

        debugLog('🚀 Disparando evento "' + eventName + '" [' + eventId + ']' + (eventIsDebug ? ' (DEBUG)' : ''), payload);

        var jsonStr = JSON.stringify(payload);

        // Prioridade: Beacon API (não bloqueia saída de página)
        if (navigator.sendBeacon) {
            try {
                var blob = new Blob([jsonStr], { type: 'application/json' });
                if (navigator.sendBeacon(apiEndpoint, blob)) {
                    return eventId;
                }
            } catch (e) {}
        }

        // Fallback: Fetch assíncrono com keepalive
        if (window.fetch) {
            try {
                fetch(apiEndpoint, {
                    method: 'POST',
                    body: jsonStr,
                    keepalive: true,
                    headers: { 'Content-Type': 'application/json' }
                }).catch(function () {});
                return eventId;
            } catch (e) {}
        }

        // Fallback legado: XHR
        try {
            var xhr = new XMLHttpRequest();
            xhr.open('POST', apiEndpoint, true);
            xhr.setRequestHeader('Content-Type', 'application/json');
            xhr.send(jsonStr);
        } catch (e) {}

        return eventId;
    }

    // 6. Ouvintes Automáticos de Interação

    // A) Disparo Automático de PageView
    function onReady() {
        trackEvent('page_view');
    }
    if (document.readyState === 'complete' || document.readyState === 'interactive') {
        onReady();
    } else {
        document.addEventListener('DOMContentLoaded', onReady);
    }

    // B) Clique em Links de WhatsApp
    document.addEventListener('click', function (e) {
        var target = e.target;
        while (target && target !== document) {
            if (target.tagName === 'A' && target.href) {
                var href = target.href.toLowerCase();
                if (href.indexOf('wa.me') !== -1 || href.indexOf('api.whatsapp.com') !== -1 || href.indexOf('whatsapp.com') !== -1) {
                    trackEvent('whatsapp_click', {}, {
                        link_url: target.href,
                        button_text: (target.innerText || target.title || '').trim().substring(0, 100)
                    });
                    break;
                }
            }
            target = target.parentNode;
        }
    }, true);

    // C) Submissão de Formulários de Contato / Lead
    document.addEventListener('submit', function (e) {
        var form = e.target;
        if (!form || form.tagName !== 'FORM') return;

        var userData = {};
        var elements = form.elements;

        for (var i = 0; i < elements.length; i++) {
            var el = elements[i];
            var name = (el.name || el.id || '').toLowerCase();
            var val = (el.value || '').trim();

            if (!val || el.type === 'password' || el.type === 'hidden') continue;

            if (name.indexOf('email') !== -1 && !userData.email) {
                userData.email = val;
            } else if ((name.indexOf('phone') !== -1 || name.indexOf('tel') !== -1 || name.indexOf('cel') !== -1 || name.indexOf('whats') !== -1) && !userData.phone) {
                userData.phone = val;
            } else if ((name.indexOf('nome') !== -1 || name.indexOf('name') !== -1) && !userData.name) {
                userData.name = val;
            }
        }

        // Se encontrou dados de lead no formulário, dispara o evento lead
        if (userData.email || userData.phone || userData.name) {
            trackEvent('lead', userData, {
                form_id: form.id || form.name || 'form_lead',
                form_action: form.action || ''
            });
        }
    }, true);

    // 7. API Pública Global
    window.hnTrack = function (eventName, options) {
        options = options || {};
        return trackEvent(eventName, options.user_data, options.custom_data);
    };

})(window, document);
