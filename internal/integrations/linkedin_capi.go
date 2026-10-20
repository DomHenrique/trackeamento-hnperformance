package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"tracking-engine/internal/collector"
	"tracking-engine/internal/identity"
)

const (
	// LinkedInAPIVersion mantido para compatibilidade retroativa como fallback
	LinkedInAPIVersion        = "202401"
	LinkedInRestliProtocolVer = "2.0.0"
	LinkedInConversionURL     = "https://api.linkedin.com/rest/conversionEvents"
)

// ResolveLinkedInAPIVersion retorna a versão ativa da API do LinkedIn no formato YYYYMM.
// Se override for informado, utiliza-o; caso contrário, busca a variável de ambiente LINKEDIN_API_VERSION.
// Se não configurado, calcula dinamicamente o mês anterior em UTC (ex: 202609), garantindo que
// a versão já esteja oficialmente publicada pelo LinkedIn (as versões têm ciclo mensal e duram no mínimo
// 12 meses, evitando falhas de NONEXISTENT_VERSION / 426 no início de um novo mês antes do LinkedIn ativá-lo).
func ResolveLinkedInAPIVersion(override ...string) string {
	if len(override) > 0 && strings.TrimSpace(override[0]) != "" {
		return strings.TrimSpace(override[0])
	}
	if envVer := strings.TrimSpace(os.Getenv("LINKEDIN_API_VERSION")); envVer != "" {
		return envVer
	}
	// Fallback padrão: Mês anterior (garantidamente publicado e com ~11 meses de suporte ativo)
	return time.Now().UTC().AddDate(0, -1, 0).Format("200601")
}

type LinkedInCAPI struct {
	http       *HTTPClient
	apiVersion string
}

func NewLinkedInCAPI(client *HTTPClient, versionOverride ...string) *LinkedInCAPI {
	ver := ""
	if len(versionOverride) > 0 {
		ver = versionOverride[0]
	}
	return &LinkedInCAPI{
		http:       client,
		apiVersion: ResolveLinkedInAPIVersion(ver),
	}
}

func (l *LinkedInCAPI) APIVersion() string {
	if l.apiVersion != "" {
		return l.apiVersion
	}
	return ResolveLinkedInAPIVersion()
}

// LinkedInErrorDiagnosis contém diagnósticos semânticos humanizados para respostas do LinkedIn
type LinkedInErrorDiagnosis struct {
	StatusCode        int    `json:"status_code"`
	FriendlyTitle     string `json:"friendly_title"`
	FriendlyMessage   string `json:"friendly_message"`
	ActionAdvice      string `json:"action_advice"`
	PropagationNotice string `json:"propagation_notice,omitempty"`
	RawError          string `json:"raw_error,omitempty"`
}

// InterpretLinkedInError analisa o status HTTP e payload de resposta do LinkedIn para gerar orientações acionáveis
func InterpretLinkedInError(statusCode int, body []byte) LinkedInErrorDiagnosis {
	var rawMsg string
	if len(body) > 0 {
		var errObj struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if err := json.Unmarshal(body, &errObj); err == nil {
			if errObj.Message != "" {
				rawMsg = errObj.Message
			} else if errObj.Code != "" {
				rawMsg = errObj.Code
			}
		} else {
			rawMsg = string(body)
		}
	}

	diag := LinkedInErrorDiagnosis{
		StatusCode: statusCode,
		RawError:   rawMsg,
	}

	switch {
	case statusCode >= 200 && statusCode < 300:
		diag.FriendlyTitle = "Sinal Aceito pelo LinkedIn"
		diag.FriendlyMessage = "A LinkedIn Conversions API recebeu o evento com sucesso."
		diag.ActionAdvice = "Acesse o Campaign Manager > Gerenciador de Sinais > Direct API para conferir a atividade."
		diag.PropagationNotice = "Importante: O LinkedIn Campaign Manager pode levar entre 5 e 15 minutos para atualizar o status da regra de 'Awaiting activity' para 'Active'."

	case statusCode == 401:
		diag.FriendlyTitle = "Token de Acesso Inválido ou Expirado"
		diag.FriendlyMessage = "O LinkedIn rejeitou a autenticação do token informado."
		diag.ActionAdvice = "Acesse o Campaign Manager > Gerenciador de Sinais > Direct API, clique em 'Gerar token de acesso' (Generate access token), copie o novo token e salve no painel."

	case statusCode == 403:
		diag.FriendlyTitle = "Permissão Insuficiente na Conta de Anúncios"
		diag.FriendlyMessage = "O usuário que gerou o token não possui permissão de Administrador ou Gestor na Conta de Anúncios vinculada."
		diag.ActionAdvice = "Verifique no Campaign Manager se o usuário do token tem papel de Administrador de Contas ou Gestor de Campanhas."

	case statusCode == 404 || statusCode == 422:
		diag.FriendlyTitle = "Regra de Conversão Não Encontrada"
		diag.FriendlyMessage = "O ID da regra de conversão informado não foi encontrado nesta conta ou o identificador é inválido."
		diag.ActionAdvice = "No Campaign Manager, acesse Mensurar > Rastreamento de conversões, localize a regra desejada e copie o ID numérico correto."

	case statusCode == 426:
		diag.FriendlyTitle = "Versão da API Inativa"
		diag.FriendlyMessage = "A versão do protocolo do LinkedIn utilizada na chamada não está mais ativa."
		diag.ActionAdvice = "O sistema aplicará a versão mensal dinâmica atual do LinkedIn automaticamente."

	case statusCode == 429:
		diag.FriendlyTitle = "Limite de Requisições Atingido (Rate Limit)"
		diag.FriendlyMessage = "O volume de chamadas por minuto da LinkedIn Conversions API foi temporariamente excedido."
		diag.ActionAdvice = "Aguarde alguns instantes antes de disparar um novo teste de envio."

	case statusCode >= 500:
		diag.FriendlyTitle = "Instabilidade Temporária no LinkedIn"
		diag.FriendlyMessage = "Os servidores do LinkedIn retornaram uma falha interna temporária."
		diag.ActionAdvice = "Aguarde alguns minutos e tente novamente. Os disparos em produção contam com retentativas automáticas."

	default:
		diag.FriendlyTitle = "Falha na Comunicação com o LinkedIn"
		diag.FriendlyMessage = fmt.Sprintf("A API do LinkedIn respondeu com status HTTP %d.", statusCode)
		diag.ActionAdvice = "Verifique as credenciais da Direct API e tente novamente."
	}

	return diag
}

type LinkedInUserId struct {
	IdType  string `json:"idType"`
	IdValue string `json:"idValue"`
}

type LinkedInUserInfo struct {
	FirstName   string `json:"firstName,omitempty"`
	LastName    string `json:"lastName,omitempty"`
	Title       string `json:"title,omitempty"`
	CompanyPage string `json:"companyPage,omitempty"`
	CompanyName string `json:"companyName,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`
}

type LinkedInUser struct {
	UserIds  []LinkedInUserId  `json:"userIds,omitempty"`
	UserInfo *LinkedInUserInfo `json:"userInfo,omitempty"`
}

type LinkedInConversionValue struct {
	CurrencyCode string `json:"currencyCode"`
	Amount       string `json:"amount"`
}

type LinkedInConversionEvent struct {
	Conversion           string                   `json:"conversion"`
	ConversionHappenedAt int64                    `json:"conversionHappenedAt"`
	ConversionValue      *LinkedInConversionValue `json:"conversionValue,omitempty"`
	User                 LinkedInUser             `json:"user"`
	EventId              string                   `json:"eventId,omitempty"`
}

// FormatConversionURN garante que o conversion rule ID esteja formatado com o prefixo URN do LinkedIn
func FormatConversionURN(ruleID string) string {
	trimmed := strings.TrimSpace(ruleID)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "urn:lla:llaPartnerConversion:") {
		return trimmed
	}
	return fmt.Sprintf("urn:lla:llaPartnerConversion:%s", trimmed)
}

// SendEvent formata e envia uma conversão qualificada para a LinkedIn Conversions API
func (l *LinkedInCAPI) SendEvent(ctx context.Context, accessToken, conversionRuleID string, ev *collector.EventPayload) error {
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("access_token ausente para LinkedIn Conversions API")
	}
	urn := FormatConversionURN(conversionRuleID)
	if urn == "" {
		return fmt.Errorf("conversion_rule_id ausente para LinkedIn Conversions API")
	}

	userIds := make([]LinkedInUserId, 0)
	if ev.UserData != nil {
		if em, ok := ev.UserData["email"].(string); ok && em != "" {
			norm := identity.NormalizeEmail(em)
			if norm != "" {
				userIds = append(userIds, LinkedInUserId{
					IdType:  "SHA256_EMAIL",
					IdValue: identity.HashSHA256(norm),
				})
			}
		}
	}

	// Suporte a LINKEDIN_FIRST_PARTY_ADS_TRACKING_UUID (li_fat_id)
	var liFatID string
	if ev.Attribution.LIFATID != "" {
		liFatID = strings.TrimSpace(ev.Attribution.LIFATID)
	}
	if liFatID == "" && ev.UserData != nil {
		if s, ok := ev.UserData["li_fat_id"].(string); ok && s != "" {
			liFatID = strings.TrimSpace(s)
		}
	}
	if liFatID == "" && ev.CustomData != nil {
		if s, ok := ev.CustomData["li_fat_id"].(string); ok && s != "" {
			liFatID = strings.TrimSpace(s)
		}
	}
	if liFatID != "" {
		userIds = append(userIds, LinkedInUserId{
			IdType:  "LINKEDIN_FIRST_PARTY_ADS_TRACKING_UUID",
			IdValue: liFatID,
		})
	}

	if len(userIds) == 0 {
		return fmt.Errorf("validação pré-despacho falhou: nenhum identificador de usuário suportado (SHA256_EMAIL ou li_fat_id) fornecido para LinkedIn Conversions API")
	}

	// Construção de user.userInfo (texto simples) com separação segura de nomes e metadados contextuais
	var userInfo *LinkedInUserInfo
	if ev.UserData != nil {
		rawName, _ := ev.UserData["name"].(string)
		rawName = strings.TrimSpace(rawName)

		var firstName, lastName string
		if rawName != "" {
			parts := strings.Fields(rawName)
			if len(parts) >= 2 {
				firstName = parts[0]
				lastName = strings.Join(parts[1:], " ")
			} else if len(parts) == 1 {
				firstName = parts[0]
			}
		}

		company, _ := ev.UserData["company"].(string)
		if company == "" {
			company, _ = ev.UserData["company_name"].(string)
		}
		company = strings.TrimSpace(company)

		title, _ := ev.UserData["title"].(string)
		if title == "" {
			title, _ = ev.UserData["job_title"].(string)
		}
		title = strings.TrimSpace(title)

		countryCode := "BR"
		if cc, ok := ev.UserData["country_code"].(string); ok && strings.TrimSpace(cc) != "" {
			countryCode = strings.ToUpper(strings.TrimSpace(cc))
		}

		if firstName != "" || lastName != "" || company != "" || title != "" {
			userInfo = &LinkedInUserInfo{
				FirstName:   firstName,
				LastName:    lastName,
				CompanyName: company,
				Title:       title,
				CountryCode: countryCode,
			}
		}
	}

	eventTimeMs := ev.EventTime.UnixMilli()
	if eventTimeMs <= 0 {
		eventTimeMs = time.Now().UnixMilli()
	}

	// Prioriza submission_id para deduplicação robusta na plataforma
	eventID := strings.TrimSpace(ev.SubmissionID)
	if eventID == "" {
		eventID = strings.TrimSpace(ev.EventID)
	}

	eventPayload := LinkedInConversionEvent{
		Conversion:           urn,
		ConversionHappenedAt: eventTimeMs,
		User: LinkedInUser{
			UserIds:  userIds,
			UserInfo: userInfo,
		},
		EventId: eventID,
	}

	// Se houver valor na conversão
	if ev.CustomData != nil {
		if val, exists := ev.CustomData["value"]; exists {
			currency := "BRL"
			if cur, ok := ev.CustomData["currency"].(string); ok && cur != "" {
				currency = strings.ToUpper(cur)
			}
			eventPayload.ConversionValue = &LinkedInConversionValue{
				CurrencyCode: currency,
				Amount:       fmt.Sprintf("%.2f", parseNumericValue(val)),
			}
		}
	}

	body, err := json.Marshal(eventPayload)
	if err != nil {
		return fmt.Errorf("erro serializando LinkedIn payload: %w", err)
	}

	candidateVersions := []string{
		l.APIVersion(),
		time.Now().UTC().AddDate(0, -1, 0).Format("200601"),
		time.Now().UTC().AddDate(0, -2, 0).Format("200601"),
		time.Now().UTC().AddDate(0, -3, 0).Format("200601"),
	}

	var lastErr error
	seenVersions := make(map[string]bool)
	for _, ver := range candidateVersions {
		if seenVersions[ver] {
			continue
		}
		seenVersions[ver] = true

		headers := map[string]string{
			"Authorization":             "Bearer " + accessToken,
			"LinkedIn-Version":          ver,
			"X-Restli-Protocol-Version": LinkedInRestliProtocolVer,
		}

		_, statusCode, err := l.http.PostWithRetry(ctx, LinkedInConversionURL, headers, body, 2)
		if statusCode == 426 {
			lastErr = fmt.Errorf("linkedin api version %s inactive (426)", ver)
			continue
		}
		if err == nil {
			l.apiVersion = ver
		}
		return err
	}

	return lastErr
}

// TestPing realiza um envio sintético para a LinkedIn Conversions API para validar conectividade e token.
// Se a versão utilizada retornar HTTP 426 (NONEXISTENT_VERSION), realiza fallback automático para versões anteriores ativas.
func (l *LinkedInCAPI) TestPing(ctx context.Context, accessToken, conversionRuleID string) ([]byte, int, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, 0, fmt.Errorf("access_token é obrigatório para testar o LinkedIn CAPI")
	}
	urn := FormatConversionURN(conversionRuleID)
	if urn == "" {
		// Se não foi informado, utiliza um URN sintético de teste
		urn = "urn:lla:llaPartnerConversion:0"
	}

	testEvent := LinkedInConversionEvent{
		Conversion:           urn,
		ConversionHappenedAt: time.Now().UnixMilli(),
		User: LinkedInUser{
			UserIds: []LinkedInUserId{
				{
					IdType:  "SHA256_EMAIL",
					IdValue: identity.HashSHA256("test@dominioexemplo.com.br"),
				},
			},
		},
		EventId: fmt.Sprintf("test_li_%d", time.Now().UnixNano()),
	}

	body, err := json.Marshal(testEvent)
	if err != nil {
		return nil, 0, fmt.Errorf("erro serializando LinkedIn payload de teste: %w", err)
	}

	candidateVersions := []string{
		l.APIVersion(),
		time.Now().UTC().AddDate(0, -1, 0).Format("200601"),
		time.Now().UTC().AddDate(0, -2, 0).Format("200601"),
		time.Now().UTC().AddDate(0, -3, 0).Format("200601"),
	}

	var lastResp []byte
	var lastStatus int
	var lastErr error

	seenVersions := make(map[string]bool)
	for _, ver := range candidateVersions {
		if seenVersions[ver] {
			continue
		}
		seenVersions[ver] = true

		headers := map[string]string{
			"Authorization":             "Bearer " + accessToken,
			"LinkedIn-Version":          ver,
			"X-Restli-Protocol-Version": LinkedInRestliProtocolVer,
		}

		resp, status, err := l.http.PostRaw(ctx, LinkedInConversionURL, headers, body)
		lastResp, lastStatus, lastErr = resp, status, err

		// Se a versão estiver inativa (426), tenta a próxima candidata
		if status == 426 {
			continue
		}

		// Se tiver sucesso ou outro status HTTP (ex: 201, 401, 403), atualiza a versão ativa do client
		l.apiVersion = ver
		return resp, status, err
	}

	return lastResp, lastStatus, lastErr
}

func parseNumericValue(val interface{}) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0.0
	}
}
