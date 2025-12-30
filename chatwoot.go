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

// --- CONFIGURAÇÃO ---
type ChatwootConfig struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
	InboxID   string `json:"inbox_id"`
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
	file, err := os.Open(configFile)
	if err == nil {
		defer file.Close()
		json.NewDecoder(file).Decode(&cwCfg)
		return
	}
	// Fallback .env
	cwCfg = ChatwootConfig{
		URL:       strings.TrimSpace(os.Getenv("CHATWOOT_URL")),
		Token:     strings.TrimSpace(os.Getenv("CHATWOOT_TOKEN")),
		AccountID: strings.TrimSpace(os.Getenv("CHATWOOT_ACCOUNT_ID")),
		InboxID:   strings.TrimSpace(os.Getenv("CHATWOOT_INBOX_ID")),
	}
}

func saveConfigToDisk(cfg ChatwootConfig) {
	cwCfgMutex.Lock()
	cwCfg = cfg
	cwCfgMutex.Unlock()
	file, _ := os.Create(configFile)
	defer file.Close()
	json.NewEncoder(file).Encode(cfg)
}

// --- ESTRUTURAS ---

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

type CreateInboxRequest struct {
	Name        string `json:"name"`
	ChannelType string `json:"channel_type"`
	WebhookUrl  string `json:"webhook_url"`
}

type CreateInboxResponse struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

type CwWebhook struct {
	Event        string `json:"event"`
	MessageType  string `json:"message_type"`
	Content      string `json:"content"`
	Conversation struct {
		ContactInbox struct {
			SourceID string `json:"source_id"`
		} `json:"contact_inbox"`
		Contact struct {
			PhoneNumber string `json:"phone_number"`
		} `json:"contact"`
	} `json:"conversation"`
}

// --- API HANDLERS ---

func sendJsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

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

// --- CRIAÇÃO AUTOMÁTICA (IGUAL EVOLUTION) ---
func (s *server) HandleAutoCreateInbox() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != os.Getenv("WUZAPI_ADMIN_TOKEN") {
			sendJsonError(w, "Token de Admin inválido", http.StatusUnauthorized)
			return
		}

		type AutoRequest struct {
			URL       string `json:"url"`
			Token     string `json:"token"`
			AccountID string `json:"account_id"`
			Name      string `json:"name"`
			WuzapiURL string `json:"wuzapi_url"`
		}
		var req AutoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sendJsonError(w, "JSON inválido", http.StatusBadRequest)
			return
		}

		// Limpa URL
		req.URL = strings.TrimSuffix(req.URL, "/")
		
		// IMPORTANTE: Aqui definimos a URL do Webhook que o Chatwoot vai chamar
		// Usamos o Admin Token na URL para garantir que o Wuzapi aceite a requisição
		webhookEndpoint := fmt.Sprintf("%s/chatwoot/webhook?token=%s", req.WuzapiURL, os.Getenv("WUZAPI_ADMIN_TOKEN"))

		cwPayload := CreateInboxRequest{
			Name:        req.Name,
			ChannelType: "api",
			WebhookUrl:  webhookEndpoint,
		}
		jsonPayload, _ := json.Marshal(cwPayload)

		// Chama API do Chatwoot
		targetURL := fmt.Sprintf("%s/api/v1/accounts/%s/inboxes", req.URL, req.AccountID)
		cwReq, _ := http.NewRequest("POST", targetURL, bytes.NewBuffer(jsonPayload))
		cwReq.Header.Set("Content-Type", "application/json")
		cwReq.Header.Set("api_access_token", req.Token)

		client := &http.Client{}
		resp, err := client.Do(cwReq)
		if err != nil {
			sendJsonError(w, "Erro de conexão: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		// Trata erro do Chatwoot (ex: 401, 404)
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			sendJsonError(w, fmt.Sprintf("Chatwoot retornou erro %d: %s", resp.StatusCode, string(body)), resp.StatusCode)
			return
		}

		var cwResp CreateInboxResponse
		if err := json.NewDecoder(resp.Body).Decode(&cwResp); err != nil {
			sendJsonError(w, "Erro ao ler resposta do Chatwoot", http.StatusInternalServerError)
			return
		}

		// Salva a configuração automaticamente
		newCfg := ChatwootConfig{
			URL:       req.URL,
			Token:     req.Token,
			AccountID: req.AccountID,
			InboxID:   strconv.Itoa(cwResp.Id),
		}
		saveConfigToDisk(newCfg)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "success",
			"inbox_id": cwResp.Id,
			"message":  fmt.Sprintf("Caixa '%s' criada com sucesso! Webhook configurado.", cwResp.Name),
		})
	}
}

// --- ENVIO (Wuzapi -> Chatwoot) ---

func SendToChatwoot(pushName string, senderUser string, text string) {
	cwCfgMutex.RLock()
	cfg := cwCfg
	cwCfgMutex.RUnlock()

	if cfg.URL == "" || cfg.Token == "" {
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
	// 1. Busca por telefone
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
		if len(searchRes.Payload) > 0 {
			return searchRes.Payload[0].ID
		}
	}
	
	// 2. Cria novo (Força source_id = phone para evitar UUID)
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
		
		// Se token for Admin, permite (usado na auto criação)
		// Caso contrario, busca a sessão do usuario
		var userID string
		if token == os.Getenv("WUZAPI_ADMIN_TOKEN") {
			// Modo simplificado: Pega o primeiro cliente conectado (já que o Evolution também opera por instancia)
			// Em um sistema multi-tenant real, precisaria mapear Inbox ID -> User ID.
			// Por enquanto, isso resolve para servidor mono-usuário.
			users := clientManager.GetLoggedInUsers()
			if len(users) > 0 {
				userID = users[0]
			} else {
				fmt.Println("[Webhook] Nenhuma sessão de WhatsApp conectada para enviar a resposta.")
				w.WriteHeader(http.StatusOK)
				return
			}
		} else {
			userInfo, found := userinfocache.Get(token)
			if !found { sendJsonError(w, "Sessão inválida", http.StatusUnauthorized); return }
			vals, ok := userInfo.(Values)
			if !ok { return }
			userID = vals.Get("Id")
		}
		
		w.WriteHeader(http.StatusOK)
		
		go func() {
			client := clientManager.GetWhatsmeowClient(userID)
			if client != nil && client.IsConnected() {
				// Prioriza o telefone real
				phone := payload.Conversation.Contact.PhoneNumber
				if phone == "" { phone = payload.Conversation.ContactInbox.SourceID }
				phone = strings.Replace(phone, "+", "", -1)
				phone = strings.Replace(phone, " ", "", -1)
				
				if len(phone) < 8 { return } // Ignora UUIDs

				jid, _ := parseJID(phone)
				client.SendMessage(context.Background(), jid, &waE2E.Message{Conversation: proto.String(payload.Content)})
			}
		}()
	}
}
