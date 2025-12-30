package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// --- CONFIGURAÇÃO COMPLETA (Baseada no Evolution) ---
type ChatwootConfig struct {
	// Conexão
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	AccountID string `json:"account_id"`
	Token     string `json:"token"`
	InboxID   string `json:"inbox_id"`

	// Comportamento
	SignMessages       bool   `json:"sign_messages"`
	SignatureDelimiter string `json:"signature_delimiter"`
	ReopenConversation bool   `json:"reopen_conversation"`
	ConversationPending bool  `json:"conversation_pending"` // Se true, desativa auto-assignment

	// Dados da Caixa
	InboxName    string `json:"inbox_name"`
	Organization string `json:"organization"`
	LogoURL      string `json:"logo_url"`

	// Importação e Filtros (Armazenados para uso futuro na lógica core)
	ImportContacts bool     `json:"import_contacts"`
	ImportMessages bool     `json:"import_messages"`
	DaysLimit      int      `json:"days_limit"`
	IgnoreJIDs     []string `json:"ignore_jids"`
}

var (
	cwCfg      ChatwootConfig
	cwCfgMutex sync.RWMutex
)

const configFile = "chatwoot.json"

func init() {
	loadConfig()
}

func loadConfig() {
	cwCfgMutex.Lock()
	defer cwCfgMutex.Unlock()
	
	// Valores padrão
	cwCfg = ChatwootConfig{
		SignatureDelimiter: "\n",
		DaysLimit:          7,
	}

	file, err := os.Open(configFile)
	if err == nil {
		defer file.Close()
		json.NewDecoder(file).Decode(&cwCfg)
		return
	}
	
	// Fallback para .env apenas para conexão básica
	cwCfg.URL = strings.TrimSpace(os.Getenv("CHATWOOT_URL"))
	cwCfg.Token = strings.TrimSpace(os.Getenv("CHATWOOT_TOKEN"))
	cwCfg.AccountID = strings.TrimSpace(os.Getenv("CHATWOOT_ACCOUNT_ID"))
	cwCfg.InboxID = strings.TrimSpace(os.Getenv("CHATWOOT_INBOX_ID"))
	cwCfg.Enabled = cwCfg.URL != ""
}

func saveConfigToDisk(cfg ChatwootConfig) {
	cwCfgMutex.Lock()
	cwCfg = cfg
	cwCfgMutex.Unlock()
	file, _ := os.Create(configFile)
	defer file.Close()
	json.NewEncoder(file).Encode(cfg)
}

// --- ESTRUTURAS CHATWOOT API ---

type CreateInboxChannel struct {
	Type       string `json:"type"`
	WebhookUrl string `json:"webhook_url"`
}

type CreateInboxRequest struct {
	Name                       string             `json:"name"`
	Channel                    CreateInboxChannel `json:"channel"`
	AllowMessagesAfterResolved bool               `json:"allow_messages_after_resolved"` // Reabrir conversa
	EnableAutoAssignment       bool               `json:"enable_auto_assignment"`        // Conversa pendente (inverso)
	Avatar                     string             `json:"avatar,omitempty"`              // Logo (URL não suportada diretamente no create por JSON simples, mas mantido estrutura)
}

type CreateInboxResponse struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

type ChatwootSearchResponse struct {
	Payload []struct {
		ID int `json:"id"`
	} `json:"payload"`
}

type ChatwootContactResponse struct {
	Payload struct {
		Contact struct {
			ID int `json:"id"`
		} `json:"contact"`
	} `json:"payload"`
}

type CwWebhook struct {
	Event        string `json:"event"`
	MessageType  string `json:"message_type"`
	Content      string `json:"content"`
	Sender       struct {
		Name string `json:"name"` // Nome do agente para assinatura
	} `json:"sender"`
	Conversation struct {
		ContactInbox struct {
			SourceID string `json:"source_id"`
		} `json:"contact_inbox"`
		Contact struct {
			PhoneNumber string `json:"phone_number"`
		} `json:"contact"`
	} `json:"conversation"`
}

func sendJsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// --- API HANDLERS ---

func (s *server) HandleSetChatwootConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != os.Getenv("WUZAPI_ADMIN_TOKEN") {
			sendJsonError(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var newCfg ChatwootConfig
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			sendJsonError(w, "JSON inválido", http.StatusBadRequest)
			return
		}
		saveConfigToDisk(newCfg)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func (s *server) HandleGetChatwootConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != os.Getenv("WUZAPI_ADMIN_TOKEN") {
			sendJsonError(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		cwCfgMutex.RLock()
		defer cwCfgMutex.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cwCfg)
	}
}

// --- AUTO CRIAÇÃO COM PARÂMETROS AVANÇADOS ---
func (s *server) HandleAutoCreateInbox() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != os.Getenv("WUZAPI_ADMIN_TOKEN") {
			sendJsonError(w, "Token Admin inválido", http.StatusUnauthorized)
			return
		}

		// Recebe a config completa do front
		var reqConfig ChatwootConfig
		if err := json.NewDecoder(r.Body).Decode(&reqConfig); err != nil {
			sendJsonError(w, "JSON inválido", http.StatusBadRequest)
			return
		}

		// Validações básicas
		sessionToken := r.URL.Query().Get("session_token") // Passado via Query ou body, vamos usar o struct se vier
		if sessionToken == "" {
			// Tenta pegar de um header customizado ou assume que veio no body se eu mudasse a struct de request
			// Para simplificar, o front vai mandar tudo dentro de ChatwootConfig, mas precisamos do session_token para o webhook
			// Vamos assumir que o front manda um campo extra ou o usuário configurou a sessão no WuzAPI
			sendJsonError(w, "Token da Sessão obrigatório na URL (?session_token=...)", http.StatusBadRequest)
			return
		}

		reqConfig.URL = strings.TrimSuffix(reqConfig.URL, "/")
		
		// Webhook
		webhookEndpoint := fmt.Sprintf("%s/chatwoot/webhook?token=%s", os.Getenv("WUZAPI_PUBLIC_URL"), sessionToken) 
		// Nota: se WUZAPI_PUBLIC_URL não existir, o front deve mandar a url. 
		// Ajuste: Vamos pegar a URL do webhook enviada pelo front num campo auxiliar se necessário, 
		// mas o padrão é o front mandar a config. Vamos usar uma struct wrapper.
	}
	// *CORREÇÃO*: Para não quebrar o handler, vou reescrever a lógica de input para aceitar o JSON misto.
	return func(w http.ResponseWriter, r *http.Request) {
		// Wrapper para receber config + session_token
		type Wrapper struct {
			Config       ChatwootConfig `json:"config"`
			SessionToken string         `json:"session_token"`
			WuzapiURL    string         `json:"wuzapi_url"`
		}
		var body Wrapper
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			sendJsonError(w, "JSON inválido", http.StatusBadRequest)
			return
		}

		if body.SessionToken == "" {
			sendJsonError(w, "Instance Token obrigatório", http.StatusBadRequest)
			return
		}

		cfg := body.Config
		cfg.URL = strings.TrimSuffix(cfg.URL, "/")
		webhookEndpoint := fmt.Sprintf("%s/chatwoot/webhook?token=%s", body.WuzapiURL, body.SessionToken)

		// Mapeia configurações do Evolution para o Chatwoot
		cwPayload := CreateInboxRequest{
			Name: cfg.InboxName,
			Channel: CreateInboxChannel{
				Type:       "api",
				WebhookUrl: webhookEndpoint,
			},
			AllowMessagesAfterResolved: cfg.ReopenConversation,     // Reabrir Conversa
			EnableAutoAssignment:       !cfg.ConversationPending,   // Se Pendente=True, AutoAssign=False
		}
		jsonPayload, _ := json.Marshal(cwPayload)

		targetURL := fmt.Sprintf("%s/api/v1/accounts/%s/inboxes", cfg.URL, cfg.AccountID)
		cwReq, _ := http.NewRequest("POST", targetURL, bytes.NewBuffer(jsonPayload))
		cwReq.Header.Set("Content-Type", "application/json")
		cwReq.Header.Set("api_access_token", cfg.Token)

		client := &http.Client{}
		resp, err := client.Do(cwReq)
		if err != nil {
			sendJsonError(w, "Erro conexão: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			sendJsonError(w, fmt.Sprintf("Erro Chatwoot (%d): %s", resp.StatusCode, string(bodyBytes)), resp.StatusCode)
			return
		}

		var cwResp CreateInboxResponse
		if err := json.NewDecoder(resp.Body).Decode(&cwResp); err != nil {
			sendJsonError(w, "Erro leitura resposta", http.StatusInternalServerError)
			return
		}

		// Atualiza ID e Salva
		cfg.InboxID = strconv.Itoa(cwResp.Id)
		saveConfigToDisk(cfg)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "success",
			"inbox_id": cwResp.Id,
			"message":  "Caixa configurada com sucesso!",
		})
	}
}

// --- ENVIO (Wuzapi -> Chatwoot) ---

func SendToChatwoot(pushName string, senderUser string, text string) {
	cwCfgMutex.RLock()
	cfg := cwCfg
	cwCfgMutex.RUnlock()

	if !cfg.Enabled || cfg.URL == "" || cfg.Token == "" {
		return
	}

	cwInboxID, _ := strconv.Atoi(cfg.InboxID)
	phoneClean := strings.Replace(senderUser, "+", "", -1)
	phoneClean = strings.Split(phoneClean, "@")[0]
	phoneNumber := "+" + phoneClean

	contactID := getOrCreateContact(cfg.URL, cfg.AccountID, cfg.Token, cwInboxID, phoneNumber, pushName)
	if contactID == 0 {
		return
	}
	sendConversation(cfg.URL, cfg.AccountID, cfg.Token, cwInboxID, contactID, text)
}

func getOrCreateContact(baseURL, accountID, token string, inboxID int, phone, name string) int {
	searchURL := fmt.Sprintf("%s/api/v1/accounts/%s/contacts/search?q=%s", baseURL, accountID, strings.Replace(phone, "+", "%2B", -1))
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("api_access_token", token)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == 200 {
		body, _ := io.ReadAll(resp.Body)
		var searchRes ChatwootSearchResponse
		json.Unmarshal(body, &searchRes)
		resp.Body.Close()
		if len(searchRes.Payload) > 0 { return searchRes.Payload[0].ID }
	}
	createURL := fmt.Sprintf("%s/api/v1/accounts/%s/contacts", baseURL, accountID)
	payload := map[string]interface{}{
		"inbox_id": inboxID, "name": name, "phone_number": phone, "source_id": phone,
	}
	jsonPayload, _ := json.Marshal(payload)
	reqCreate, _ := http.NewRequest("POST", createURL, bytes.NewBuffer(jsonPayload))
	reqCreate.Header.Set("Content-Type", "application/json")
	reqCreate.Header.Set("api_access_token", token)
	respCreate, err := client.Do(reqCreate)
	if err != nil { return 0 }
	defer respCreate.Body.Close()
	if respCreate.StatusCode == 200 {
		var contactRes ChatwootContactResponse
		json.NewDecoder(respCreate.Body).Decode(&contactRes)
		return contactRes.Payload.Contact.ID
	}
	return 0
}

func sendConversation(baseURL, accountID, token string, inboxID, contactID int, text string) {
	url := fmt.Sprintf("%s/api/v1/accounts/%s/conversations", baseURL, accountID)
	payload := map[string]interface{}{
		"inbox_id": inboxID, "contact_id": contactID, "status": "open",
		"message": map[string]string{"content": text, "message_type": "incoming"},
	}
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", token)
	client := &http.Client{}
	resp, _ := client.Do(req)
	if resp != nil { resp.Body.Close() }
}

// --- WEBHOOK (Chatwoot -> Wuzapi) ---

func (s *server) HandleChatwootWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" { sendJsonError(w, "Token necessário", http.StatusUnauthorized); return }
		
		var payload CwWebhook
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil { w.WriteHeader(http.StatusOK); return }
		
		if payload.Event != "message_created" || payload.MessageType != "outgoing" { w.WriteHeader(http.StatusOK); return }
		
		// Carrega config para checar assinatura
		cwCfgMutex.RLock()
		cfg := cwCfg
		cwCfgMutex.RUnlock()

		userInfo, found := userinfocache.Get(token)
		if !found { w.WriteHeader(http.StatusOK); return }
		
		w.WriteHeader(http.StatusOK)
		
		go func() {
			vals, ok := userInfo.(Values)
			if !ok { return }
			userID := vals.Get("Id")
			client := clientManager.GetWhatsmeowClient(userID)
			if client != nil && client.IsConnected() {
				phone := payload.Conversation.Contact.PhoneNumber
				if phone == "" { phone = payload.Conversation.ContactInbox.SourceID }
				phone = strings.Replace(phone, "+", "", -1)
				phone = strings.Replace(phone, " ", "", -1)
				phone = strings.TrimSpace(phone)
				if len(phone) < 8 { return }

				// LÓGICA DE ASSINATURA
				finalMessage := payload.Content
				if cfg.SignMessages && payload.Sender.Name != "" {
					delimiter := cfg.SignatureDelimiter
					if delimiter == "" { delimiter = "\n" }
					// Processa caracteres de escape como \n
					delimiter = strings.ReplaceAll(delimiter, `\n`, "\n")
					finalMessage = fmt.Sprintf("%s%s%s", finalMessage, delimiter, payload.Sender.Name)
				}

				jid, _ := parseJID(phone)
				client.SendMessage(context.Background(), jid, &waE2E.Message{Conversation: proto.String(finalMessage)})
			}
		}()
	}
}
