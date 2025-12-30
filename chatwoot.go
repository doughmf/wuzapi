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
	// Fallback para variáveis de ambiente
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

// --- UTILITÁRIOS ---

func sendJsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// --- API HANDLERS (CONFIGURAÇÃO) ---

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

// --- AUTO-CRIAÇÃO (A MÁGICA) ---
func (s *server) HandleAutoCreateInbox() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Validação
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

		// 2. Preparação dos dados
		req.URL = strings.TrimSuffix(req.URL, "/")
		
		// NOTA: Para funcionar, o webhook URL precisa ter um token VÁLIDO DE INSTÂNCIA.
		// Como não sabemos qual instância o usuário quer usar aqui (o painel é genérico), 
		// vamos deixar um placeholder ou usar o token admin e tratar no webhook.
		// IMPORTANTE: O usuário DEVE garantir que o token na URL do webhook seja de uma instância conectada.
		webhookEndpoint := fmt.Sprintf("%s/chatwoot/webhook?token=%s", req.WuzapiURL, os.Getenv("WUZAPI_ADMIN_TOKEN"))

		cwPayload := CreateInboxRequest{
			Name:        req.Name,
			ChannelType: "api",
			WebhookUrl:  webhookEndpoint,
		}
		jsonPayload, _ := json.Marshal(cwPayload)

		// 3. Chamada ao Chatwoot
		targetURL := fmt.Sprintf("%s/api/v1/accounts/%s/inboxes", req.URL, req.AccountID)
		cwReq, _ := http.NewRequest("POST", targetURL, bytes.NewBuffer(jsonPayload))
		cwReq.Header.Set("Content-Type", "application/json")
		cwReq.Header.Set("api_access_token", req.Token)

		client := &http.Client{}
		resp, err := client.Do(cwReq)
		if err != nil {
			sendJsonError(w, "Erro de conexão com Chatwoot: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		// 4. Tratamento de Erros do Chatwoot
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			sendJsonError(w, fmt.Sprintf("Chatwoot recusou (HTTP %d): %s", resp.StatusCode, string(body)), resp.StatusCode)
			return
		}

		// 5. Sucesso - Salva configuração
		var cwResp CreateInboxResponse
		if err := json.NewDecoder(resp.Body).Decode(&cwResp); err != nil {
			sendJsonError(w, "Erro ao ler resposta do Chatwoot", http.StatusInternalServerError)
			return
		}

		newCfg := ChatwootConfig{
			URL:       req.URL,
			Token:     req.Token,
			AccountID: req.AccountID,
			InboxID:   strconv.Itoa(cwResp.Id),
		}
		saveConfigToDisk(newCfg)

		// 6. Resposta Final
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "success",
			"inbox_id": cwResp.Id,
			"message":  fmt.Sprintf("Caixa '%s' criada com sucesso! ID: %d. ATENÇÃO: Verifique se o Webhook no Chatwoot está com o token da sua instância.", cwResp.Name, cwResp.Id),
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
	// A. Busca contato existente pelo telefone
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
	
	// B. Cria novo contato se não existir
	// source_id = phone é CRUCIAL para o Chatwoot identificar corretamente no webhook de volta
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
		
		// Aqui removemos a lógica complexa de "GetLoggedInUsers" que causou o erro
		// O webhook DEVE conter um token válido de uma sessão existente.
		userInfo, found := userinfocache.Get(token)
		
		if !found { 
			// Se o token não for encontrado (ex: foi usado o admin token na criação auto),
			// logamos o erro, mas retornamos 200 OK pro Chatwoot não ficar tentando de novo infinitamente.
			fmt.Printf("[Chatwoot Webhook] Erro: Token '%s' não corresponde a nenhuma sessão ativa.\n", token)
			w.WriteHeader(http.StatusOK)
			return 
		}
		
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
				
				if len(phone) < 8 { 
					fmt.Printf("[Chatwoot] Ignorando envio para ID inválido: %s\n", phone)
					return 
				}

				jid, _ := parseJID(phone)
				client.SendMessage(context.Background(), jid, &waE2E.Message{Conversation: proto.String(payload.Content)})
			} else {
				fmt.Println("[Chatwoot] Cliente WhatsApp não conectado.")
			}
		}()
	}
}
