/**
 * HN Performance Server-Side Tracker SDK v1.0
 * Lightweight (< 4KB), zero-dependencies, 1st-party tracking & attribution.
 */
(function (window, document) {
    'use strict';

    function extractKeyFromUrl(url) {
        if (!url || typeof url !== 'string') return '';
        var match = url.match(/[?&](?:site_key|key)=([a-zA-Z0-9_-]+)/);
        return match ? match[1] : '';
    }

    // 1. Identificação Robusta do Script e Configurações (com suporte nativo ao GTM)
    function resolveSiteKey() {
        if (window.__HN_SITE_KEY__) return String(window.__HN_SITE_KEY__).trim();
        if (document.currentScript) {
            if (document.currentScript.getAttribute('data-site-key')) {
                return document.currentScript.getAttribute('data-site-key').trim();
            }
            var fromCurrSrc = extractKeyFromUrl(document.currentScript.src);
            if (fromCurrSrc) return fromCurrSrc;
        }
        var anyElement = document.querySelector('[data-site-key]');
        if (anyElement && anyElement.getAttribute('data-site-key')) {
            return anyElement.getAttribute('data-site-key').trim();
        }
        var allScripts = document.getElementsByTagName('script');
        for (var i = allScripts.length - 1; i >= 0; i--) {
            var s = allScripts[i];
            var k = s.getAttribute('data-site-key');
            if (k) return k.trim();
            var fromSrc = extractKeyFromUrl(s.src || s.getAttribute('data-gtmsrc'));
            if (fromSrc) return fromSrc;
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

    // 3. Gestão de Sessão (hn_ses_...) em sessionStorage volátil (Cookieless)
    var SESSION_KEY = '_hn_sid';
    var isNewSession = false;
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

        var lang = nav.language || nav.userLanguage || '';
        var tz = '';
        try {
            tz = Intl.DateTimeFormat().resolvedOptions().timeZone || '';
        } catch (e) {}

        return {
            webdriver: isWebdriver,
            headless: isHeadless,
            screen_w: scr.width || 0,
            screen_h: scr.height || 0,
            screen_res: (scr.width && scr.height) ? (scr.width + 'x' + scr.height) : '',
            color_depth: scr.colorDepth || 0,
            lang: lang,
            tz: tz,
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

        // Arquitetura Puramente Cookieless:
        // A identidade do visitante (visitor_id) é calculada deterministicamente no servidor via HMAC-SHA256 (sinais de rede + hardware + salt diário).
        // Nenhum cookie de visitante (_vid) é lido, gravado ou transmitido pelo SDK no navegador.
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

    // C) Submissão de Formulários de Contato / Lead com Ciclo de Vida e Blocklist
    var lastSubmission = { submission_id: '', form_id: '', time: 0 };

    function generateSubmissionId() {
        if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
            try {
                return crypto.randomUUID();
            } catch (e) {}
        }
        return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function (c) {
            var r = (Math.random() * 16) | 0;
            var v = c === 'x' ? r : (r & 0x3 | 0x8);
            return v.toString(16);
        });
    }

    // 1. Hard Blocklist de Campos Sensíveis (avaliada antes de qualquer acesso a .value)
    function isFieldBlocked(el) {
        if (!el) return true;
        var tag = (el.tagName || '').toUpperCase();
        if (tag === 'TEXTAREA' || tag === 'BUTTON' || tag === 'OBJECT' || tag === 'EMBED') {
            return true;
        }

        var type = (el.type || '').toLowerCase();
        if (type === 'password' || type === 'file' || type === 'hidden' || type === 'submit' || type === 'reset' || type === 'button') {
            return true;
        }

        var auto = (el.getAttribute('autocomplete') || el.autocomplete || '').toLowerCase();
        if (auto.indexOf('cc-') !== -1 || auto.indexOf('credit') !== -1 || auto.indexOf('card') !== -1 || auto.indexOf('cvc') !== -1 || auto.indexOf('cvv') !== -1) {
            return true;
        }

        var nameId = ((el.name || '') + ' ' + (el.id || '')).toLowerCase();
        if (nameId.indexOf('token') !== -1 || nameId.indexOf('nonce') !== -1 || nameId.indexOf('csrf') !== -1 || nameId.indexOf('captcha') !== -1 || nameId.indexOf('recaptcha') !== -1) {
            return true;
        }

        return false;
    }

    // 2. Mapeamento Explícito e Heurística Semântica em Cascata
    function classifyField(el) {
        if (!el) return { role: null, source: '' };

        // 2.1 Mapeamento Explícito: data-hn-field (prioridade máxima se não bloqueado)
        var explicitAttr = (el.getAttribute('data-hn-field') || '').toLowerCase().trim();
        if (explicitAttr === 'email' || explicitAttr === 'phone' || explicitAttr === 'name') {
            return { role: explicitAttr, source: 'explicit' };
        }

        // 2.2 Heurística: autocomplete
        var auto = (el.getAttribute('autocomplete') || el.autocomplete || '').toLowerCase().trim();
        if (auto) {
            if (auto === 'email' || auto.indexOf('email') !== -1) {
                return { role: 'email', source: 'autocomplete' };
            }
            if (auto === 'tel' || auto.indexOf('tel') !== -1 || auto.indexOf('phone') !== -1) {
                return { role: 'phone', source: 'autocomplete' };
            }
            if (auto === 'name' || auto === 'given-name' || auto === 'family-name' || auto.indexOf('name') !== -1) {
                return { role: 'name', source: 'autocomplete' };
            }
        }

        // 2.3 Heurística: type
        var type = (el.type || '').toLowerCase().trim();
        if (type === 'email') {
            return { role: 'email', source: 'type' };
        }
        if (type === 'tel') {
            return { role: 'phone', source: 'type' };
        }

        // 2.4 Heurística: placeholder
        var placeholder = (el.getAttribute('placeholder') || el.placeholder || '').toLowerCase().trim();
        if (placeholder) {
            if (placeholder.indexOf('email') !== -1 || placeholder.indexOf('e-mail') !== -1 || placeholder.indexOf('correo') !== -1) {
                return { role: 'email', source: 'placeholder' };
            }
            if (placeholder.indexOf('telefone') !== -1 || placeholder.indexOf('phone') !== -1 || placeholder.indexOf('tel') !== -1 || placeholder.indexOf('celular') !== -1 || placeholder.indexOf('whats') !== -1) {
                return { role: 'phone', source: 'placeholder' };
            }
            if (placeholder.indexOf('nome') !== -1 || placeholder.indexOf('name') !== -1 || placeholder.indexOf('first_name') !== -1) {
                return { role: 'name', source: 'placeholder' };
            }
        }

        // 2.5 Heurística: aria-label
        var ariaLabel = (el.getAttribute('aria-label') || '').toLowerCase().trim();
        if (ariaLabel) {
            if (ariaLabel.indexOf('email') !== -1 || ariaLabel.indexOf('e-mail') !== -1 || ariaLabel.indexOf('correo') !== -1) {
                return { role: 'email', source: 'aria-label' };
            }
            if (ariaLabel.indexOf('telefone') !== -1 || ariaLabel.indexOf('phone') !== -1 || ariaLabel.indexOf('tel') !== -1 || ariaLabel.indexOf('celular') !== -1 || ariaLabel.indexOf('whats') !== -1) {
                return { role: 'phone', source: 'aria-label' };
            }
            if (ariaLabel.indexOf('nome') !== -1 || ariaLabel.indexOf('name') !== -1 || ariaLabel.indexOf('first_name') !== -1) {
                return { role: 'name', source: 'aria-label' };
            }
        }

        // 2.6 Heurística Legada: name / id
        var nameId = ((el.name || '') + ' ' + (el.id || '')).toLowerCase().trim();
        if (nameId) {
            if (nameId.indexOf('email') !== -1 || nameId.indexOf('e-mail') !== -1 || nameId.indexOf('correo') !== -1) {
                return { role: 'email', source: 'name-id-heuristic' };
            }
            if (nameId.indexOf('phone') !== -1 || nameId.indexOf('telefone') !== -1 || nameId.indexOf('tel') !== -1 || nameId.indexOf('cel') !== -1 || nameId.indexOf('whats') !== -1) {
                return { role: 'phone', source: 'name-id-heuristic' };
            }
            if (nameId.indexOf('nome') !== -1 || nameId.indexOf('name') !== -1 || nameId.indexOf('first_name') !== -1) {
                return { role: 'name', source: 'name-id-heuristic' };
            }
        }

        return { role: null, source: '' };
    }

    document.addEventListener('submit', function (e) {
        var form = e.target;
        if (!form || form.tagName !== 'FORM') return;

        var submissionId = generateSubmissionId();
        var userData = {};
        var fieldSources = {};
        var elements = form.elements;

        for (var i = 0; i < elements.length; i++) {
            var el = elements[i];
            
            // Hard Blocklist avaliada ANTES de qualquer leitura de .value
            if (isFieldBlocked(el)) continue;

            var classification = classifyField(el);
            if (!classification.role) continue;

            var val = (el.value || '').trim();
            if (!val) continue;

            if (!userData[classification.role]) {
                userData[classification.role] = val;
                fieldSources[classification.role] = classification.source;
            }
        }

        // Se encontrou dados de contato no formulário, dispara ciclo de vida
        if (userData.email || userData.phone || userData.name) {
            var domFormId = form.id || form.name || form.getAttribute('data-form-id') || 'form_lead';
            var dedupKey = (userData.email || userData.phone || userData.name) + '_' + domFormId;
            if (shouldDeduplicateForm(dedupKey)) return;

            lastSubmission = {
                submission_id: submissionId,
                form_id: domFormId,
                time: Date.now(),
                has_error: false
            };

            // 1. Estado form_attempt (event_name consistente 'form_submit', estágio 'form_attempt')
            trackEvent('form_submit', userData, {
                submission_id: submissionId,
                form_lifecycle_state: 'form_attempt',
                field_source: JSON.stringify(fieldSources),
                form_id: domFormId,
                form_action: form.action || '',
                trigger_source: 'dom_submit'
            });

            // 2. Estado form_client_validated (se passou na validação nativa do navegador - sem re-envio de PII)
            if (typeof form.checkValidity === 'function' && form.checkValidity()) {
                trackEvent('form_submit', {}, {
                    submission_id: submissionId,
                    form_lifecycle_state: 'form_client_validated',
                    form_id: domFormId,
                    trigger_source: 'dom_validation'
                });
            }
        }
    }, true);

    // D) Deduplicação e Auto-Interceptação de Eventos do Google Tag Manager (dataLayer)
    var lastCapturedForm = { time: 0, key: '' };
    function shouldDeduplicateForm(key) {
        var now = Date.now();
        if (lastCapturedForm.key === key && (now - lastCapturedForm.time) < 3000) {
            return true;
        }
        lastCapturedForm = { time: now, key: key };
        return false;
    }

    function inspectDataLayerItem(item) {
        if (!item || typeof item !== 'object') return;
        var evt = (item.event || '').toLowerCase();
        if (!evt) return;

        // D.1) Detecção de Erro do Builder ou Falha AJAX (impede que eventos de erro disparem conversão de sucesso)
        var isErrorEvent = evt === 'bricks/form/submit/error' ||
                           evt === 'bricks/form/error' ||
                           evt === 'elementor/form/submit/error' ||
                           evt === 'elementor/form/error' ||
                           evt === 'wpforms_error' ||
                           evt === 'fluentform_submission_error' ||
                           evt === 'wpcf7mailfailed' ||
                           evt === 'wpcf7invalid' ||
                           evt === 'wpcf7spam' ||
                           evt === 'form_error' ||
                           evt === 'form_submit_error';

        if (isErrorEvent) {
            debugLog('Builder reportou falha/erro na submissão do formulário:', evt);
            if (lastSubmission) {
                lastSubmission.has_error = true;
            }
            return;
        }

        // D.2) Detecção de Confirmação de Sucesso de Builders (Bricks, Elementor, WPForms)
        var isSuccessEvent = evt === 'bricks/form/submit/success' ||
                             evt === 'elementor/form/submit/success' ||
                             evt === 'wpforms_completed' ||
                             evt === 'fluentform_submission_success' ||
                             evt === 'wpcf7mailsent' ||
                             evt === 'form_success' ||
                             evt === 'form_submit_success';

        if (isSuccessEvent) {
            var nowTime = Date.now();
            // Se a submissão anterior foi marcada com erro, não gera sucesso falso
            if (lastSubmission && lastSubmission.has_error && !item.submission_id) {
                debugLog('Ignorando evento de sucesso pois a submissão foi previamente marcada com erro.');
                return;
            }

            var successSubId = item.submission_id || ((nowTime - lastSubmission.time < 30000) ? lastSubmission.submission_id : '') || generateSubmissionId();
            var successFormId = item.form_id || item.formId || lastSubmission.form_id || 'builder_success_form';

            debugLog('Capturado evento de sucesso do servidor via dataLayer:', evt, successSubId);
            trackEvent('form_submit', {}, {
                submission_id: successSubId,
                form_lifecycle_state: 'form_submit_success',
                form_id: successFormId,
                trigger_source: 'builder_event_' + evt
            });
            return;
        }

        // D.3) Captura de Submissão de Formulário via dataLayer
        var isFormEvent = evt === 'form_submit' || 
                           evt === 'form_submission' || 
                           evt === 'lead' || 
                           evt === 'generate_lead' || 
                           evt === 'envio de formulário' || 
                           evt === 'envio de formulario' ||
                           evt === 'contato' || 
                           evt === 'contact';

        if (isFormEvent) {
            var email = item.email || item.mail || item.dlv_email || item.user_email || '';
            var phone = item.telefone || item.phone || item.tel || item.celular || item.whatsapp || item.dlv_telefone || '';
            var name = item.nome || item.name || item.first_name || item.dlv_nome || '';

            if (item.user_data && typeof item.user_data === 'object') {
                email = email || item.user_data.email || '';
                phone = phone || item.user_data.phone || item.user_data.telefone || '';
                name = name || item.user_data.name || item.user_data.nome || '';
            }

            var formId = item.form_id || item.formId || item.id || 'gtm_datalayer_form';
            var dedupKey = (email || phone || name) + '_' + formId;
            if (shouldDeduplicateForm(dedupKey)) {
                debugLog('Deduplicação: ignorando disparo duplicado de formulário dataLayer em < 3s');
                return;
            }

            var submissionId = item.submission_id || generateSubmissionId();
            var userData = {};
            var fieldSources = {};
            if (email) { userData.email = String(email).trim(); fieldSources.email = 'datalayer'; }
            if (phone) { userData.phone = String(phone).trim(); fieldSources.phone = 'datalayer'; }
            if (name) { userData.name = String(name).trim(); fieldSources.name = 'datalayer'; }

            lastSubmission = {
                submission_id: submissionId,
                form_id: formId,
                time: Date.now(),
                has_error: false
            };

            var customData = {
                submission_id: submissionId,
                form_lifecycle_state: 'form_attempt',
                field_source: JSON.stringify(fieldSources),
                form_id: formId,
                page_url: item.page_url || window.location.href,
                page_title: item.page_title || document.title,
                trigger_source: 'datalayer_' + evt
            };

            debugLog('Capturado evento de formulário via dataLayer:', evt, userData, customData);
            trackEvent('form_submit', userData, customData);
        }
    }

    function setupDataLayerListener() {
        if (typeof window === 'undefined') return;

        function hook(dl) {
            if (!dl || dl.__hn_hooked) return;
            dl.__hn_hooked = true;

            // Inspeciona itens já enfileirados
            for (var i = 0; i < dl.length; i++) {
                inspectDataLayerItem(dl[i]);
            }

            var origPush = dl.push;
            dl.push = function () {
                var res = origPush.apply(dl, arguments);
                for (var j = 0; j < arguments.length; j++) {
                    try {
                        inspectDataLayerItem(arguments[j]);
                    } catch (e) {
                        debugLog('Erro ao inspecionar push do dataLayer:', e);
                    }
                }
                return res;
            };
        }

        window.dataLayer = window.dataLayer || [];
        hook(window.dataLayer);
    }

    setupDataLayerListener();

    // 7. API Pública Global (Eventos e Consentimento)
    window.hnTrack = function (actionOrName, options, extraData) {
        if (actionOrName === 'consent') {
            updateConsent(options);
            return;
        }
        if (actionOrName === 'formSuccess') {
            if (lastSubmission && lastSubmission.has_error && (!options || !options.submission_id)) {
                debugLog('[HN Tracker] formSuccess ignorado: submissão anterior foi sinalizada com erro.');
                return;
            }
            var subId = (options && options.submission_id) || ((Date.now() - lastSubmission.time < 30000) ? lastSubmission.submission_id : '') || generateSubmissionId();
            var fId = (options && options.form_id) || lastSubmission.form_id || 'manual_success';
            return trackEvent('form_submit', {}, {
                submission_id: subId,
                form_lifecycle_state: 'form_submit_success',
                form_id: fId,
                trigger_source: 'api_form_success'
            });
        }
        options = options || {};
        var uData = options.user_data;
        var cData = options.custom_data;
        if (!uData && !cData) {
            uData = options;
            cData = extraData || {};
        }
        return trackEvent(actionOrName, uData || {}, cData || {});
    };

})(window, document);
