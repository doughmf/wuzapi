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
	cwCfg = ChatwootConfig{
		URL:       strings.TrimSpace(os.Getenv("CHATWOOT_URL")),
		Token:     strings.TrimSpace(os.Getenv("CHATWOOT_TOKEN")),
		AccountID: strings.TrimSpace(os.Getenv("CHATWOOT_ACCOUNT_ID")),
		InboxID:   strings.TrimSpace(os.Getenv("CHATWOOT_INBOX_ID")),
	}
}

// --- ESTRUTURAS API CHATWOOT ---
type CreateInboxRequest struct {
	Name        string `json:"name"`
	ChannelType string `json:"channel_type"` // Sempre "api"
	WebhookUrl  string `json:"webhook_url"`
}

type CreateInboxResponse struct {
	Id          int    `json:"id"`
	Name        string `json:"name"`
	AccessToken string `json:"access_token"` // Token da Inbox (não usado aqui, usamos o de usuário)
}

// --- API HANDLERS DO WUZAPI ---

// 1. Salvar Config Manual
func (s *server) HandleSetChatwootConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != os.Getenv("WUZAPI_ADMIN_TOKEN") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var newCfg ChatwootConfig
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, "JSON inválido", http.StatusBadRequest)
			return
		}
		saveConfigToDisk(newCfg)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "Configuração salva!"})
	}
}

// 2. Ler Config
func (s *server) HandleGetChatwootConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != os.Getenv("WUZAPI_ADMIN_TOKEN") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		cwCfgMutex.RLock()
		defer cwCfgMutex.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cwCfg)
	}
}

// 3. AUTO-CRIAR CAIXA DE ENTRADA (NOVO!)
func (s *server) HandleAutoCreateInbox() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Valida Admin
		if r.Header.Get("Authorization") != os.Getenv("WUZAPI_ADMIN_TOKEN") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Lê dados enviados pelo front (URL, Token, AccountID, Nome da Inbox)
		type AutoRequest struct {
			URL       string `json:"url"`
			Token     string `json:"token"`
			AccountID string `json:"account_id"`
			Name      string `json:"name"`
			WuzapiURL string `json:"wuzapi_url"` // URL pública deste Wuzapi para o webhook
		}
		var req AutoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "JSON inválido", http.StatusBadRequest)
			return
		}

		// Monta o Webhook URL (Wuzapi -> Chatwoot -> Wuzapi)
		// O token na URL do webhook DEVE ser o token da instância (pegamos o primeiro disponível ou admin)
		// Simplificação: Vamos usar o Admin Token na URL do webhook para garantir que chegue
		webhookEndpoint := fmt.Sprintf("%s/chatwoot/webhook?token=%s", req.WuzapiURL, os.Getenv("WUZAPI_ADMIN_TOKEN"))

		// Prepara payload para o Chatwoot
		cwPayload := CreateInboxRequest{
			Name:        req.Name,
			ChannelType: "api",
			WebhookUrl:  webhookEndpoint,
		}
		jsonPayload, _ := json.Marshal(cwPayload)

		// Faz a requisição ao Chatwoot
		targetURL := fmt.Sprintf("%s/api/v1/accounts/%s/inboxes", req.URL, req.AccountID)
		cwReq, _ := http.NewRequest("POST", targetURL, bytes.NewBuffer(jsonPayload))
		cwReq.Header.Set("Content-Type", "application/json")
		cwReq.Header.Set("api_access_token", req.Token)

		client := &http.Client{}
		resp, err := client.Do(cwReq)
		if err != nil {
			http.Error(w, "Erro ao conectar no Chatwoot: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			http.Error(w, "Erro do Chatwoot: "+string(body), resp.StatusCode)
			return
		}

		// Decodifica resposta (Pega o ID da nova caixa)
		var cwResp CreateInboxResponse
		if err := json.NewDecoder(resp.Body).Decode(&cwResp); err != nil {
			http.Error(w, "Erro ao ler resposta do Chatwoot", http.StatusInternalServerError)
			return
		}

		// Salva tudo automaticamente na configuração
		newCfg := ChatwootConfig{
			URL:       req.URL,
			Token:     req.Token,
			AccountID: req.AccountID,
			InboxID:   strconv.Itoa(cwResp.Id),
		}
		saveConfigToDisk(newCfg)

		// Responde sucesso
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "success",
			"inbox_id": cwResp.Id,
			"message":  "Caixa criada e webhook configurado com sucesso!",
		})
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

// --- FUNÇÕES DE ENVIO E RECEBIMENTO (MANTIDAS IGUAIS) ---
// (Mantenha aqui as funções SendToChatwoot, getOrCreateContact, sendConversation e HandleChatwootWebhook
// que já estavam funcionando no código anterior. Não altere essa parte final.)

type CwWebhook struct {
	Event        string `json:"event"`
	MessageType  string `json:"message_type"`
	Content      string `json:"content"`
	Conversation struct {
		ContactInbox struct {
			SourceID string `json:"source_id"`
		} `json:"contact_inbox"`
	} `json:"conversation"`
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

func SendToChatwoot(pushName string, senderUser string, text string) {
	cwCfgMutex.RLock()
	cfg := cwCfg
	cwCfgMutex.RUnlock()

	if cfg.URL == "" || cfg.Token == "" {
		return
	}

	cwInboxID, _ := strconv.Atoi(cfg.InboxID)
	phoneClean := strings.Replace(senderUser, "+", "", -1)
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
		if len(searchRes.Payload) > 0 {
			return searchRes.Payload[0].ID
		}
	}
	createURL := fmt.Sprintf("%s/api/v1/accounts/%s/contacts", baseURL, accountID)
	payload := map[string]interface{}{"inbox_id": inboxID, "name": name, "phone_number": phone}
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

func (s *server) HandleChatwootWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" { http.Error(w, "Unauthorized", http.StatusUnauthorized); return }
		var payload CwWebhook
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil { w.WriteHeader(http.StatusOK); return }
		if payload.Event != "message_created" || payload.MessageType != "outgoing" { w.WriteHeader(http.StatusOK); return }
		
		userInfo, found := userinfocache.Get(token)
		if !found { w.WriteHeader(http.StatusUnauthorized); return }
		
		w.WriteHeader(http.StatusOK) // Responde rápido
		
		go func() {
			vals, ok := userInfo.(Values)
			if !ok { return }
			client := clientManager.GetWhatsmeowClient(vals.Get("Id"))
			if client != nil && client.IsConnected() {
				phone := strings.Replace(payload.Conversation.ContactInbox.SourceID, "+", "", -1)
				jid, _ := parseJID(phone)
				client.SendMessage(context.Background(), jid, &waE2E.Message{Conversation: proto.String(payload.Content)})
			}
		}()
	}
}
