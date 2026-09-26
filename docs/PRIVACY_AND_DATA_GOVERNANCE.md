# Especificação de Governança de Dados e Privacidade (Privacy by Design)
**HN Performance Tracking Engine**  
*Documento de Especificação Técnica, Governança e Guia do Controlador (LGPD / ANPD / GDPR)*  
*Versão: 1.0 — Data: Setembro de 2026*

---

## 1. Introdução e Filosofia de Arquitetura

O **HN Tracking Engine** foi concebido sob a metodologia de **Privacy by Design e Privacy by Default**. Diferente de rastreadores legados que realizam coleta irrestrita de dados e presumem consentimento implícito, esta plataforma estabelece uma separação estrita entre:

1. **Os Meios Técnicos (Operador):** O engine fornece infraestrutura segura de borda, ingestão, filas, persistência e despacho de eventos, garantindo isolamento entre clientes (*multi-tenant*), integridade e mecanismos de expurgo.
2. **As Decisões de Tratamento (Controlador):** O cliente (proprietário do site rastreado) atua como Controlador perante a Lei Geral de Proteção de Dados (LGPD - Lei nº 13.709/2018), definindo as finalidades concretas, as bases legais cabíveis, a política de cookies exibida aos usuários e a configuração de privacidade ativada na plataforma.

> [!IMPORTANT]
> **O código da plataforma não fixa nem presume a base legal dos tratamentos.** Cabe ao Controlador selecionar a hipótese legal adequada (Consentimento, Legítimo Interesse, Execução de Contrato, etc.) para cada finalidade, garantir que cookies não necessários permaneçam desativados até a manifestação positiva do titular (conforme orientação da ANPD) e disponibilizar política de privacidade transparente aos seus visitantes.

---

## 2. Inventário e Matriz de Dados (Data Mapping)

A tabela abaixo descreve cada dado processado pela plataforma, sua fonte de coleta, finalidade técnica pretendida, local de persistência, compartilhamento e nível de necessidade técnica.

| Campo / Dado | Origem | Finalidade Técnica | Armazenamento | Compartilhamento | Necessidade | Atenção / Salvaguarda de Privacidade |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **`visitor_id`** | Cookie HTTP de 1ª parte (`_vid`) gerado pelo servidor | Agrupar eventos de um mesmo navegador ao longo do tempo para atribuição de jornada. | Cookie 1st-party + Postgres (`visitors`) + ClickHouse (`events`) | Não compartilhado com terceiros em formato bruto. | **Opcional** (Depende de escolha de Analytics) | **Identificador pseudônimo:** Não torna o dado anônimo. Trata-se de dado pessoal indireto que permite individualizar o visitante. |
| **`session_id`** | `sessionStorage` do navegador (`_hn_sid`) | Agrupar eventos ocorridos em uma mesma janela de navegação ativa. | `sessionStorage` + ClickHouse (`events`) | Não compartilhado. | **Opcional** (Telemetria de sessão) | Expirado automaticamente ao fechar a aba/sessão do navegador. |
| **URL da Página (`page_url`)** | Objeto `window.location.href` | Mensurar quais conteúdos e páginas foram visualizados. | ClickHouse (`events`) + Postgres (`first_landing_page`) | Apenas metadados de conversão via Dispatcher. | **Necessário** para analytics | **Risco de Vazamento de PII:** Parâmetros de URL não devem conter e-mails, tokens ou documentos. O coletor deve higienizar query strings sensíveis. |
| **Referrer (`referrer`)** | Objeto `document.referrer` | Identificar o site ou mecanismo de busca de onde o visitante se originou. | ClickHouse (`events`) + Postgres (`first_referrer`) | Não compartilhado. | **Opcional** (Origem de tráfego) | O coletor descarta query strings de referrers externos para evitar vazamentos de contexto pessoal. |
| **IP do Cliente (`ip_address`)** | Conexão TCP remota da requisição HTTP | Geolocalização técnica aproximada, bloqueio de robôs/ataques de negação de serviço. | ClickHouse (`events`) | Encaminhado no Dispatcher (Meta CAPI / Google) apenas se consentido. | **Necessário** para segurança de rede | **Dado Pessoal:** Sujeito a mascaramento (truncamento de último octeto IPv4 / 80 bits IPv6) quando configurado pelo cliente. |
| **User-Agent** | Cabeçalho HTTP `User-Agent` | Identificação de dispositivo (Mobile, Desktop) e heurística de detecção de bots. | ClickHouse (`events`) | Encaminhado em eventos de conversão (Meta/Google). | **Necessário** para segurança/render | Utilizado para auditoria de tráfego fraudulento. |
| **Parâmetros de Campanha (`utm_*`, `gclid`, `fbclid`, `ttclid`)** | Parâmetros de URL capturados | Avaliar o retorno sobre investimento de anúncios pagos e realizar deduplicação de conversões. | `localStorage` temporário + ClickHouse + Postgres | Compartilhado com a respectiva rede de anúncios de origem (Meta, Google, TikTok). | **Opcional** (Marketing) | Exige categoria de consentimento `marketing` ativada. Sujeito a expurgo periódico. |
| **Dados de Contato (`email`, `phone`, `name`)** | Formulários HTML de submissão voluntária | Reconhecimento de lead e conversão qualificada no funil comercial. | Postgres (`visitors` sob HMAC com chave secreta de aplicação) | Convertido para SHA-256 no Dispatcher para APIs de Conversão (Meta/Google/CRM). | **Opcional** (Específico de Conversão) | **Nunca coletado passivamente ou por padrão.** Coletado apenas no evento de envio explícito de formulário pelo titular. |
| **Registro de Consentimento (`consent`)** | Banner CMP ou cabeçalhos (`Sec-GPC`, `DNT`) | Demonstrar a configuração aplicada e bloquear categorias não autorizadas. | Payload do evento + Postgres (log de auditoria de escolha) | Não compartilhado. | **Necessário** para conformidade | Registro mínimo de auditoria (versão da escolha, categorias concedidas, data/hora, origem). |

---

## 3. Matriz de Finalidades e Hipóteses Legais por Operação

O tratamento de dados dentro do HN Tracking Engine é particionado em **5 finalidades operacionais distintas**. Cabe ao Controlador (cliente) mapear suas operações concretas para as hipóteses legais da LGPD (Art. 7º):

```
┌────────────────────────────────────────────────────────────────────────┐
│                   MATRIZ DE FINALIDADES OPERACIONAIS                   │
├────────────────────────────────────────────────────────────────────────┤
│ 1. Segurança & Prevenção a Fraudes    ──▶ Exclusão de bots, rate limit │
│ 2. Medição Agregada de Uso do Site    ──▶ Contagem de visitas e erros  │
│ 3. Análise de Jornada e Retenção      ──▶ Navegação multi-sessão (_vid)│
│ 4. Atribuição e Mensuração de Mídia   ──▶ UTMs, GCLID, FBCLID          │
│ 5. Encaminhamento de Conversões (Ads) ──▶ Despacho Meta CAPI/Google MP │
└────────────────────────────────────────────────────────────────────────┘
```

### Detalhamento por Operação

#### Operação 1: Segurança, Prevenção a Fraudes e Diagnóstico Técnico
- **Controlador Responsável:** O cliente do site (com suporte técnico da operadora).
- **Hipótese Legal Sugerida:** Art. 7º, IX (Legítimo Interesse) ou Art. 7º, II (Cumprimento de Obrigação Legal - ex: Art. 15 do Marco Civil da Internet para guarda de registros de conexão).
- **Dados Utilizados:** Endereço IP da conexão, User-Agent, cabeçalhos de rede, timestamps e sinais heurísticos de automação (`client_signals`).
- **Destinatários:** Nenhum (processamento estritamente interno na infraestrutura do coletor).
- **Retenção Recomendada:** 30 a 90 dias em registros detalhados de tráfego.

#### Operação 2: Medição Agregada de Audiência (Métricas Essenciais)
- **Controlador Responsável:** O cliente do site.
- **Hipótese Legal Sugerida:** Art. 7º, IX (Legítimo Interesse), condicionado a testes de proporcionalidade (LIA - *Legitimate Interests Assessment*) e salvaguardas (IP mascarado, sem cruzamento com identidades externas).
- **Dados Utilizados:** Páginas acessadas, tipo de dispositivo, país/região agregada, tempo de carregamento.
- **Destinatários:** Dashboard analítico exclusivo do próprio cliente.
- **Retenção Recomendada:** 12 a 24 meses em formato agregado.

#### Operação 3: Análise de Jornada Multi-Sessão (Identificador Persistente `_vid`)
- **Controlador Responsável:** O cliente do site.
- **Hipótese Legal Sugerida:** Art. 7º, I (Consentimento - categoria `analytics`), seguindo as diretrizes da ANPD para armazenamento em terminal de usuário via cookies de 1ª parte não estritamente necessários.
- **Dados Utilizados:** `visitor_id`, histórico de navegação interna, timestamps de primeira e última visita.
- **Destinatários:** Uso analítico interno do cliente.
- **Retenção Recomendada:** Máximo de 12 meses a partir da última atividade do visitante.

#### Operação 4: Atribuição de Campanhas Publicitárias (UTMs e Click IDs)
- **Controlador Responsável:** O cliente do site.
- **Hipótese Legal Sugerida:** Art. 7º, I (Consentimento - categoria `marketing`).
- **Dados Utilizados:** Parâmetros `utm_source`, `utm_medium`, `utm_campaign`, `gclid`, `fbclid`, `ttclid`.
- **Destinatários:** Plataformas analíticas do cliente e redes de anúncios onde as campanhas foram veiculadas.
- **Retenção Recomendada:** 90 a 180 dias.

#### Operação 5: Despacho Server-Side de Conversões (Meta CAPI, Google Ads, LinkedIn, CRMs)
- **Controlador Responsável:** O cliente do site.
- **Hipótese Legal Sugerida:** Art. 7º, I (Consentimento específico para compartilhamento com terceiros) ou Art. 7º, V (Execução de Contrato / procedimentos preliminares, quando o titular envia seus dados voluntariamente para solicitar orçamento/aquisição).
- **Dados Utilizados:** Valor da transação, identificador de evento (`event_id`), dados de contato pseudonimizados (SHA-256 de e-mail e telefone), IP e User-Agent.
- **Destinatários:** Meta Platforms Inc., Google LLC, LinkedIn Corporation e provedores de CRM indicados pelo cliente.
- **Retenção no Egress:** Transitório nas filas Redis Stream (descarte imediato após confirmação de entrega pelo destino).

---

## 4. Orientações e Boas Práticas aos Sites Clientes

O HN Tracking Engine fornece os instrumentos técnicos, mas a segurança e conformidade dependem criticamente das boas práticas operacionais do cliente:

### A) Proibição Rigorosa de Dados Pessoais em URLs
- **Regra:** Nunca transmita e-mails, nomes, números de CPF, telefones ou senhas em parâmetros de URL (`GET` query parameters).
  - *Exemplo Proibido:* `https://meusite.com.br/obrigado?email=joao@exemplo.com&cpf=12345678900`
  - *Forma Adequada:* Transmita tais identificadores no corpo da requisição (`POST`) em campos estruturados sob HTTPS.
- O coletor da plataforma implementa filtros preventivos que descartam ou ofuscam parâmetros suspeitos (`email=`, `cpf=`, `token=`), mas a responsabilidade primária de higienização na origem é do site cliente.

### B) Gestão de Banners de Consentimento (CMPs)
- A ANPD orienta que cookies não estritamente necessários não devem ser acionados antes do aceite voluntário e informado do usuário.
- O site cliente deve integrar sua CMP (OneTrust, Cookiebot, Usercentrics, AdOpt, etc.) com o SDK da plataforma utilizando o método unificado:
  ```javascript
  // Disparado imediatamente após a escolha do usuário no banner
  window.hnTrack('consent', {
    necessary: true,
    analytics: userConsent.analytics === true,
    marketing: userConsent.marketing === true
  });
  ```
- O padrão do SDK antes da manifestação positiva do usuário deve ser o **modo restrito** (sem gravação de cookies ou envio de parâmetros de publicidade).

---

## 5. Atendimento aos Direitos dos Titulares (Art. 18 LGPD)

A LGPD assegura aos titulares os direitos de confirmação de tratamento, acesso, correção, anonimização, bloqueio, eliminação e revogação de consentimento.

### A) Localização de Registros de um Titular
A plataforma indexa e isola os dados por `site_id`. Para atender a um pedido de exclusão ou acesso, o cliente deve fornecer:
1. O identificador do site (`site_id`);
2. O identificador de visitante (`visitor_id` gravado no cookie `_vid` do navegador do titular) **OU** o hash HMAC do e-mail/telefone informado pelo titular em formulários.

### B) Mapeamento de Onde os Dados Residem
```
┌───────────────────────┬────────────────────────────────────────────────────────┐
│ Repositório           │ Ação de Eliminação / Expurgo                           │
├───────────────────────┼────────────────────────────────────────────────────────┤
│ **PostgreSQL**        │ `DELETE FROM visitors WHERE site_id = $1 AND ...`      │
│ (`visitors`)          │ Remove o perfil consolidado, first-touch e hashes.     │
├───────────────────────┼────────────────────────────────────────────────────────┤
│ **ClickHouse**        │ `ALTER TABLE events DELETE WHERE site_id = $1 AND ...` │
│ (`events`)            │ Marca eventos analíticos para exclusão via mutação.    │
├───────────────────────┼────────────────────────────────────────────────────────┤
│ **Redis / Filas**     │ Expiração natural (tempo de vida em fila < 2 horas).   │
├───────────────────────┼────────────────────────────────────────────────────────┤
│ **Logs de Aplicação** │ Rotação diária e descarte total em até 30 dias.        │
├───────────────────────┼────────────────────────────────────────────────────────┤
│ **Backups Frios**     │ Retenção máxima de 90 dias com descarte automático.    │
└───────────────────────┴────────────────────────────────────────────────────────┘
```

> [!NOTE]
> **Salvaguarda de Restauração de Backups:** Se um backup prévio for restaurado para recuperação de desastre, a plataforma mantém um registro histórico de exclusões solicitadas (*Tombstones*) para reexecutar automaticamente a purga dos titulares que já haviam exercido seu direito de eliminação.

---

## 6. Segurança da Informação e Segregação de Tenants

Em observância aos requisitos de segurança para agentes de tratamento (Art. 46 da LGPD e Guia de Segurança da Informação da ANPD):

1. **Segregação Multi-Tenant Estrita:**
   - Toda consulta no banco relacional e analítico exige a cláusula obrigatória `site_id = ?`. Um cliente jamais acessa dados ou métricas de outro tenant.
2. **Criptografia em Trânsito:**
   - Todas as transmissões do SDK para a API e da API para serviços externos utilizam TLS 1.3 obrigatório com certificados válidos e suporte a HSTS.
3. **Pseudonimização com HMAC e Pepper:**
   - Dados de contato gravados no PostgreSQL utilizam HMAC-SHA256 com *pepper* secreto da aplicação gerenciado via variável de ambiente protegida (`APP_PEPPER`).
4. **Proteção Contra Spoofing de Rede:**
   - Cabeçalhos de proxy (`X-Forwarded-For`, `CF-Connecting-IP`) são sumariamente ignorados a menos que a conexão TCP direta se origine de subnets expressamente homologadas em `TRUSTED_PROXIES`.
5. **Procedimento para Incidentes de Segurança:**
   - Em caso de incidente com potencial risco aos titulares, a operadora notificará formalmente os controladores afetados no prazo de até 48 horas após a confirmação do evento, contendo a natureza dos dados, medidas de contenção tomadas e orientações técnicas.
