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

// --- ESTRUTURAS ---

// Configuração do Chatwoot (Salva em arquivo)
type ChatwootConfig struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
	InboxID   string `json:"inbox_id"`
}

// Variável global para guardar a configuração na memória
var (
	cwCfg      ChatwootConfig
	cwCfgMutex sync.RWMutex
)

// Nome do arquivo de configuração
const configFile = "chatwoot.json"

// Inicializa lendo do arquivo ou do ENV
func init() {
	loadConfig()
}

// Carrega configuração
func loadConfig() {
	cwCfgMutex.Lock()
	defer cwCfgMutex.Unlock()

	// 1. Tenta ler do arquivo JSON
	file, err := os.Open(configFile)
	if err == nil {
		defer file.Close()
		decoder := json.NewDecoder(file)
		if err := decoder.Decode(&cwCfg); err == nil {
			fmt.Println("[Chatwoot] Configuração carregada do arquivo chatwoot.json")
			return
		}
	}

	// 2. Se falhar, usa variáveis de ambiente (.env)
	fmt.Println("[Chatwoot] Arquivo de config não encontrado, usando .env")
	cwCfg = ChatwootConfig{
		URL:       strings.TrimSpace(os.Getenv("CHATWOOT_URL")),
		Token:     strings.TrimSpace(os.Getenv("CHATWOOT_TOKEN")),
		AccountID: strings.TrimSpace(os.Getenv("CHATWOOT_ACCOUNT_ID")),
		InboxID:   strings.TrimSpace(os.Getenv("CHATWOOT_INBOX_ID")),
	}
}

// --- API: DEFINIR CONFIGURAÇÃO ---
func (s *server) HandleSetChatwootConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Valida Token Wuzapi
		if r.Header.Get("Authorization") != os.Getenv("WUZAPI_ADMIN_TOKEN") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var newCfg ChatwootConfig
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, "JSON inválido", http.StatusBadRequest)
			return
		}

		// Salva na memória
		cwCfgMutex.Lock()
		cwCfg = newCfg
		cwCfgMutex.Unlock()

		// Salva no arquivo
		file, err := os.Create(configFile)
		if err != nil {
			http.Error(w, "Erro ao salvar arquivo", http.StatusInternalServerError)
			return
		}
		defer file.Close()
		json.NewEncoder(file).Encode(newCfg)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "Configuração do Chatwoot atualizada com sucesso!"})
	}
}

// --- API: LER CONFIGURAÇÃO ATUAL ---
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

// --- INTEGRAÇÃO ---

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
	Conversation struct {
		ContactInbox struct {
			SourceID string `json:"source_id"`
		} `json:"contact_inbox"`
	} `json:"conversation"`
}

// --- ENVIAR (Wuzapi -> Chatwoot) ---
func SendToChatwoot(pushName string, senderUser string, text string) {
	cwCfgMutex.RLock()
	cfg := cwCfg // Copia a config atual para uso local seguro
	cwCfgMutex.RUnlock()

	if cfg.URL == "" || cfg.Token == "" {
		fmt.Println("[Chatwoot] ERRO: Integração não configurada (Use a API /chatwoot/config para configurar).")
		return
	}

	cwInboxID, _ := strconv.Atoi(cfg.InboxID)
	phoneClean := strings.Replace(senderUser, "+", "", -1)
	phoneNumber := "+" + phoneClean

	contactID := getOrCreateContact(cfg.URL, cfg.AccountID, cfg.Token, cwInboxID, phoneNumber, pushName)
	if contactID == 0 {
		fmt.Println("[Chatwoot] Falha ao obter ID do contato.")
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
	payload := map[string]interface{}{
		"inbox_id":     inboxID,
		"name":         name,
		"phone_number": phone,
	}
	jsonPayload, _ := json.Marshal(payload)

	reqCreate, _ := http.NewRequest("POST", createURL, bytes.NewBuffer(jsonPayload))
	reqCreate.Header.Set("Content-Type", "application/json")
	reqCreate.Header.Set("api_access_token", token)

	respCreate, err := client.Do(reqCreate)
	if err != nil {
		return 0
	}
	defer respCreate.Body.Close()

	if respCreate.StatusCode == 200 {
		body, _ := io.ReadAll(respCreate.Body)
		var contactRes ChatwootContactResponse
		json.Unmarshal(body, &contactRes)
		return contactRes.Payload.Contact.ID
	}
	return 0
}

func sendConversation(baseURL, accountID, token string, inboxID, contactID int, text string) {
	url := fmt.Sprintf("%s/api/v1/accounts/%s/conversations", baseURL, accountID)
	payload := map[string]interface{}{
		"inbox_id":   inboxID,
		"contact_id": contactID,
		"status":     "open",
		"message": map[string]string{
			"content":      text,
			"message_type": "incoming",
		},
	}

	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Printf("[Chatwoot] Mensagem enviada (ID %d)\n", contactID)
	} else {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("[Chatwoot] FALHA (Erro %d): %s\n", resp.StatusCode, string(body))
	}
}

// --- RECEBER (Chatwoot -> Wuzapi) ---

func (s *server) HandleChatwootWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Token obrigatório", http.StatusUnauthorized)
			return
		}

		var payload CwWebhook
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusOK) 
			return
		}

		if payload.Event != "message_created" || payload.MessageType != "outgoing" {
			w.WriteHeader(http.StatusOK)
			return
		}

		userInfo, found := userinfocache.Get(token)
		if !found {
			http.Error(w, "Sessão inválida", http.StatusUnauthorized)
			return
		}

		// Responde OK rápido
		w.WriteHeader(http.StatusOK)

		// Processa em background
		go func() {
			vals, ok := userInfo.(Values)
			if !ok { return }
			userID := vals.Get("Id")
			client := clientManager.GetWhatsmeowClient(userID)

			if client == nil || !client.IsConnected() {
				fmt.Println("[Webhook] WhatsApp desconectado")
				return
			}

			phone := strings.Replace(payload.Conversation.ContactInbox.SourceID, "+", "", -1)
			jid, _ := parseJID(phone)
			
			fmt.Printf("[Chatwoot -> WhatsApp] Enviando para %s: %s\n", phone, payload.Content)
			client.SendMessage(context.Background(), jid, &waE2E.Message{
				Conversation: proto.String(payload.Content),
			})
		}()
	}
}
