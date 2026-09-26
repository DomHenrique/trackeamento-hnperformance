/**
 * HN Performance Server-Side Tracker SDK v1.0
 * Lightweight (< 4KB), zero-dependencies, 1st-party tracking & attribution.
 */
(function (window, document) {
    'use strict';

    // 1. Identificação Robusta do Script e Configurações (com suporte nativo ao GTM)
    function resolveSiteKey() {
        if (window.__HN_SITE_KEY__) return String(window.__HN_SITE_KEY__).trim();
        if (document.currentScript && document.currentScript.getAttribute('data-site-key')) {
            return document.currentScript.getAttribute('data-site-key').trim();
        }
        var anyElement = document.querySelector('[data-site-key]');
        if (anyElement && anyElement.getAttribute('data-site-key')) {
            return anyElement.getAttribute('data-site-key').trim();
        }
        // GTM e injeções assíncronas podem armazenar atributos em scripts já existentes no DOM
        var taggedScripts = document.querySelectorAll('script[data-site-key]');
        if (taggedScripts && taggedScripts.length > 0) {
            return taggedScripts[taggedScripts.length - 1].getAttribute('data-site-key').trim();
        }
        var allScripts = document.getElementsByTagName('script');
        for (var i = allScripts.length - 1; i >= 0; i--) {
            var k = allScripts[i].getAttribute('data-site-key');
            if (k) return k.trim();
        }
        return '';
    }

    var currentScript = document.currentScript || (function () {
        var scripts = document.getElementsByTagName('script');
        for (var i = scripts.length - 1; i >= 0; i--) {
            if (scripts[i].getAttribute('data-site-key') || 
                (scripts[i].src && scripts[i].src.indexOf('/sdk/tracker.js') !== -1) ||
                scripts[i].getAttribute('data-gtmsrc')) {
                return scripts[i];
            }
        }
        return scripts[scripts.length - 1];
    })();

    var siteKey = resolveSiteKey().replace(/^["'`]|["'`]$/g, '');

    function resolveApiEndpoint() {
        if (window.__HN_ENDPOINT__) return String(window.__HN_ENDPOINT__).trim();
        if (currentScript && currentScript.getAttribute('data-endpoint')) {
            return currentScript.getAttribute('data-endpoint').trim();
        }
        var epScript = document.querySelector('script[data-endpoint], [data-endpoint]');
        if (epScript && epScript.getAttribute('data-endpoint')) {
            return epScript.getAttribute('data-endpoint').trim();
        }
        // Extrai o host de onde o tracker.js foi carregado (inclui suporte a data-gtmsrc do GTM)
        var srcScript = document.querySelector('script[src*="/sdk/tracker.js"], script[data-gtmsrc*="/sdk/tracker.js"]');
        if (srcScript) {
            var rawSrc = srcScript.src || srcScript.getAttribute('data-gtmsrc') || '';
            if (rawSrc && rawSrc.indexOf('/sdk/tracker.js') !== -1) {
                return rawSrc.replace(/\/sdk\/tracker\.js.*$/, '/api/v1/collect');
            }
        }
        if (currentScript && currentScript.src && currentScript.src.indexOf('/sdk/tracker.js') !== -1) {
            return currentScript.src.replace(/\/sdk\/tracker\.js.*$/, '/api/v1/collect');
        }
        // Fallback seguro: se carregado em site cliente externo, envia sempre para o coletor oficial da HN
        return 'https://trackeamento.hnperformancedigital.com.br/api/v1/collect';
    }

    var apiEndpoint = resolveApiEndpoint();

    // Validação defensiva do formato da chave para alertar desenvolvedores
    if (siteKey) {
        if (!siteKey.startsWith('hn_site_') && !siteKey.startsWith('hn_live_key_')) {
            console.warn('[HN Tracker] AVISO: A chave informada em data-site-key ("' + siteKey + '") não utiliza o padrão "hn_site_...". Verifique se você não colou um identificador de visitante (hn_vis_) ou outro parâmetro por engano.');
        }
    }

    // Detecção Robusta de Modo Debug (GTM Preview, Tag Assistant, URL params, sessionStorage, script attr ou global)
    var isDebug = false;
    try {
        var fullUrl = window.location.href || '';
        var searchStr = window.location.search || '';
        var winName = window.name || '';
        var rawCookies = document.cookie || '';

        var isGTMPreview = fullUrl.indexOf('gtm_debug=') !== -1 || 
                           searchStr.indexOf('gtm_debug=') !== -1 || 
                           (document.referrer && document.referrer.indexOf('tagassistant.google.com') !== -1) ||
                           window.__TAG_ASSISTANT_DEBUG__ === true ||
                           winName.indexOf('goog_tag_assistant_') !== -1 ||
                           winName.indexOf('TAG_ASSISTANT') !== -1 ||
                           winName.indexOf('gtm_debug') !== -1 ||
                           rawCookies.indexOf('gtm_debug=') !== -1 ||
                           rawCookies.indexOf('__gtm_preview=') !== -1 ||
                           rawCookies.indexOf('_tag_assistant=') !== -1;

        var hasUrlDebug = fullUrl.indexOf('hn_debug=true') !== -1 || 
                          fullUrl.indexOf('hn_debug=1') !== -1 || 
                          fullUrl.indexOf('debug_mode=1') !== -1 || 
                          fullUrl.indexOf('debug_mode=true') !== -1;
        var hasSessionDebug = sessionStorage.getItem('_hn_debug') === '1';
        var hasScriptDebug = (currentScript && (currentScript.getAttribute('data-debug') === 'true' || currentScript.getAttribute('data-debug') === '1')) ||
                             !!document.querySelector('script[data-debug="true"]');
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
        debugLog('⚡ Modo Debug ativo! Eventos serão transmitidos ao vivo para o DebugView.');
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

    // 3. Gestão de Sessão (hn_ses_...) e Detecção de Primeira Visita (GA4 Lifecycle)
    var SESSION_KEY = '_hn_sid';
    var FIRST_VISIT_KEY = '_hn_first_visit';
    var isNewSession = false;
    var isFirstVisit = false;
    var sessionId = null;

    try {
        sessionId = sessionStorage.getItem(SESSION_KEY);
        if (!sessionId) {
            sessionId = generatePrefixedId('hn_ses_', 24);
            sessionStorage.setItem(SESSION_KEY, sessionId);
            isNewSession = true;
        }
    } catch (e) {
        sessionId = generatePrefixedId('hn_ses_', 24);
        isNewSession = true;
    }

    try {
        if (!localStorage.getItem(FIRST_VISIT_KEY)) {
            isFirstVisit = true;
            localStorage.setItem(FIRST_VISIT_KEY, String(Date.now()));
        }
    } catch (e) {}

    // 4. Gestão de Governança de Privacidade, Consentimento e Sinais do Navegador (GPC)
    var CONSENT_STORAGE_KEY = '_hn_consent';
    var isGPCActive = false;
    try {
        var nav = window.navigator || {};
        if (nav.globalPrivacyControl === true || window.globalPrivacyControl === true) {
            isGPCActive = true;
        }
    } catch (e) {}

    var userConsent = {
        necessary: true,
        analytics: true,
        marketing: !isGPCActive
    };

    try {
        var rawConsent = localStorage.getItem(CONSENT_STORAGE_KEY);
        if (rawConsent) {
            var parsed = JSON.parse(rawConsent);
            if (typeof parsed.analytics === 'boolean') userConsent.analytics = parsed.analytics;
            if (typeof parsed.marketing === 'boolean') userConsent.marketing = parsed.marketing;
            if (isGPCActive) userConsent.marketing = false; // GPC prevalece restritivamente
        }
    } catch (e) {}

    function updateConsent(newConsent) {
        if (!newConsent || typeof newConsent !== 'object') return;
        if (typeof newConsent.analytics === 'boolean') userConsent.analytics = newConsent.analytics;
        if (typeof newConsent.marketing === 'boolean') userConsent.marketing = newConsent.marketing;
        if (isGPCActive) userConsent.marketing = false;

        try {
            localStorage.setItem(CONSENT_STORAGE_KEY, JSON.stringify(userConsent));
        } catch (e) {}

        if (!userConsent.marketing) {
            try {
                localStorage.removeItem(ATTR_STORAGE_KEY);
            } catch (e) {}
        } else {
            persistURLParams();
        }

        debugLog('🔒 Preferências de privacidade atualizadas:', userConsent);
    }

    // 5. Captura e Persistência de Atribuição (UTMs e Click IDs sob Consentimento de Marketing)
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

    function persistURLParams() {
        if (!userConsent.marketing) return; // Modo restrito: não persiste parâmetros de publicidade sem consentimento
        var current = parseURLParams();
        var stored = {};
        try {
            var rawStored = localStorage.getItem(ATTR_STORAGE_KEY);
            if (rawStored) stored = JSON.parse(rawStored);
        } catch (e) {}

        for (var k in current) {
            if (current.hasOwnProperty(k)) stored[k] = current[k];
        }
        try {
            localStorage.setItem(ATTR_STORAGE_KEY, JSON.stringify(stored));
        } catch (e) {}
    }

    persistURLParams();

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
            siteKey = resolveSiteKey().replace(/^["'`]|["'`]$/g, '');
        }
        if (!siteKey) {
            console.warn('[HN Tracker] data-site-key não configurada.');
            return;
        }
        if (!apiEndpoint || apiEndpoint === '/api/v1/collect') {
            apiEndpoint = resolveApiEndpoint();
        }

        var eventId = generatePrefixedId('hn_evt_', 24); // Gerado no cliente para deduplicação com Meta Pixel / Google CAPI
        userData = userData || {};
        customData = customData || {};

        // A identidade do visitante (visitor_id) é gerida com autoridade exclusiva pelo servidor via cookie HTTP de 1ª parte (_vid).
        // Remove qualquer visitor_id arbitrário enviado pelo cliente para impedir Session Fixation e respeitar a limpeza de cookies pelo usuário.
        if (userData && userData.visitor_id) {
            delete userData.visitor_id;
        }

        // Injeta cookies Meta caso presentes e permitidos por consentimento de marketing
        if (userConsent.marketing) {
            var fbp = getCookie('_fbp');
            var fbc = getCookie('_fbc');
            if (fbp && !userData.fbp) userData.fbp = fbp;
            if (fbc && !userData.fbc) userData.fbc = fbc;
        }

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
            consent: {
                necessary: true,
                analytics: !!userConsent.analytics,
                marketing: !!userConsent.marketing
            },
            is_debug: !!eventIsDebug
        };

        debugLog('🚀 Disparando evento "' + eventName + '" [' + eventId + ']' + (eventIsDebug ? ' (DEBUG)' : ''), payload);

        var jsonStr = JSON.stringify(payload);

        // Prioridade: Beacon API (não bloqueia saída de página e evita preflight complexo com text/plain)
        if (navigator.sendBeacon) {
            try {
                var blob = new Blob([jsonStr], { type: 'text/plain;charset=UTF-8' });
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
                    mode: 'cors',
                    credentials: 'include',
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

    // 6. Ouvintes Automáticos de Interação & Enhanced Measurement (Padrão GA4)

    // A) Ciclo de Vida da Sessão e PageView
    function onReady() {
        if (isFirstVisit) {
            trackEvent('first_visit', {}, {
                first_visit_time: localStorage.getItem(FIRST_VISIT_KEY) || String(Date.now())
            });
        }
        if (isNewSession) {
            trackEvent('session_start', {}, {
                session_id: sessionId
            });
        }
        trackEvent('page_view', {}, {
            page_title: document.title || '',
            page_location: window.location.href
        });
    }

    if (document.readyState === 'complete' || document.readyState === 'interactive') {
        onReady();
    } else {
        document.addEventListener('DOMContentLoaded', onReady);
    }

    // B) Rolagem Profunda de 90% (Scroll Depth - Padrão GA4)
    var scrollTracked = false;
    var scrollThrottleTimer = null;

    function checkScrollDepth() {
        if (scrollTracked) return;
        try {
            var docEl = document.documentElement || document.body;
            var scrollTop = window.pageYOffset || docEl.scrollTop || 0;
            var winHeight = window.innerHeight || docEl.clientHeight || 0;
            var docHeight = Math.max(
                docEl.scrollHeight || 0,
                docEl.offsetHeight || 0,
                docEl.clientHeight || 0
            );

            if (docHeight > winHeight) {
                var percent = Math.round(((scrollTop + winHeight) / docHeight) * 100);
                if (percent >= 90) {
                    scrollTracked = true;
                    trackEvent('scroll', {}, {
                        percent_scrolled: 90,
                        page_title: document.title || '',
                        page_location: window.location.href
                    });
                    if (window.removeEventListener) {
                        window.removeEventListener('scroll', throttledScrollCheck, { passive: true });
                    }
                }
            }
        } catch (e) {}
    }

    function throttledScrollCheck() {
        if (scrollTracked) return;
        if (!scrollThrottleTimer) {
            scrollThrottleTimer = setTimeout(function () {
                scrollThrottleTimer = null;
                checkScrollDepth();
            }, 250);
        }
    }

    if (window.addEventListener) {
        window.addEventListener('scroll', throttledScrollCheck, { passive: true });
    }

    // C) Cliques em Links: WhatsApp, Outbound Links e Downloads de Arquivos
    document.addEventListener('click', function (e) {
        var target = e.target;
        while (target && target !== document) {
            if (target.tagName === 'A' && target.href) {
                var rawHref = target.href;
                var lowerHref = rawHref.toLowerCase();
                var linkText = (target.innerText || target.title || '').trim().substring(0, 100);

                // 1. WhatsApp
                if (lowerHref.indexOf('wa.me') !== -1 || lowerHref.indexOf('api.whatsapp.com') !== -1 || lowerHref.indexOf('whatsapp.com') !== -1) {
                    trackEvent('whatsapp_click', {}, {
                        link_url: rawHref,
                        button_text: linkText
                    });
                    break;
                }

                // 2. Download de Arquivos (.pdf, .xlsx, .docx, .zip, etc.)
                var fileRegex = /\.(pdf|xlsx?|docx?|pptx?|zip|rar|csv|txt|mp3|mp4|exe|dmg|apk|gz)$/i;
                var cleanPath = (target.pathname || '').split('?')[0].split('#')[0];
                if (fileRegex.test(cleanPath)) {
                    var extMatch = cleanPath.match(fileRegex);
                    var ext = extMatch ? extMatch[1].toLowerCase() : '';
                    var fileName = cleanPath.substring(cleanPath.lastIndexOf('/') + 1);
                    trackEvent('file_download', {}, {
                        file_name: fileName,
                        file_extension: ext,
                        link_url: rawHref,
                        link_text: linkText
                    });
                    break;
                }

                // 3. Outbound Link (Clique de Saída para Domínio Externo)
                var targetHost = (target.hostname || '').toLowerCase();
                var currentHost = (window.location.hostname || '').toLowerCase();
                if (targetHost && targetHost !== currentHost && 
                    lowerHref.indexOf('javascript:') !== 0 && 
                    lowerHref.indexOf('mailto:') !== 0 && 
                    lowerHref.indexOf('tel:') !== 0) {
                    trackEvent('click', {}, {
                        outbound: true,
                        link_url: rawHref,
                        link_domain: targetHost,
                        link_text: linkText
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

    // 7. API Pública Global (Eventos e Consentimento)
    window.hnTrack = function (actionOrName, options) {
        if (actionOrName === 'consent') {
            updateConsent(options);
            return;
        }
        options = options || {};
        return trackEvent(actionOrName, options.user_data, options.custom_data);
    };

})(window, document);
