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

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// --- ESTRUTURAS DE DADOS ---

// Resposta de busca do Chatwoot
type ChatwootSearchResponse struct {
	Payload []struct {
		ID int `json:"id"`
	} `json:"payload"`
}

// Resposta de criação de contato
type ChatwootContactResponse struct {
	Payload struct {
		Contact struct {
			ID int `json:"id"`
		} `json:"contact"`
	} `json:"payload"`
}

// Webhook recebido do Chatwoot
type CwWebhook struct {
	Event        string `json:"event"`
	MessageType  string `json:"message_type"`
	Content      string `json:"content"`
	Conversation struct {
		ContactInbox struct {
			SourceID string `json:"source_id"` // O número do telefone (+55...)
		} `json:"contact_inbox"`
	} `json:"conversation"`
}

// --- FUNÇÃO 1: ENVIAR PARA CHATWOOT (Wuzapi -> Chatwoot) ---

func SendToChatwoot(pushName string, senderUser string, text string) {
	cwURL := strings.TrimSpace(os.Getenv("CHATWOOT_URL"))
	cwToken := strings.TrimSpace(os.Getenv("CHATWOOT_TOKEN"))
	cwAccountID := strings.TrimSpace(os.Getenv("CHATWOOT_ACCOUNT_ID"))
	cwInboxIDStr := strings.TrimSpace(os.Getenv("CHATWOOT_INBOX_ID"))

	if cwURL == "" || cwToken == "" {
		fmt.Println("[Chatwoot] ERRO: Integração não configurada.")
		return
	}

	cwInboxID, _ := strconv.Atoi(cwInboxIDStr)
	phoneNumber := "+" + senderUser

	// 1. Obter o CONTACT_ID (Busca ou Cria) - Essencial para Chatwoot 4.9+
	contactID := getOrCreateContact(cwURL, cwAccountID, cwToken, cwInboxID, phoneNumber, pushName)
	if contactID == 0 {
		fmt.Println("[Chatwoot] Falha ao obter ID do contato. Abortando envio.")
		return
	}

	// 2. Enviar Mensagem
	sendConversation(cwURL, cwAccountID, cwToken, cwInboxID, contactID, text)
}

// Auxiliar: Busca ou cria o contato
func getOrCreateContact(baseURL, accountID, token string, inboxID int, phone, name string) int {
	// A. Buscar
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

	// B. Criar
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
		fmt.Printf("[Chatwoot] Erro ao criar contato: %v\n", err)
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

// Auxiliar: Envia a mensagem
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
		fmt.Println("[Chatwoot] Erro de conexão:", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Printf("[Chatwoot] Mensagem enviada (Contact ID: %d)\n", contactID)
	} else {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("[Chatwoot] FALHA (Erro %d): %s\n", resp.StatusCode, string(body))
	}
}

// --- FUNÇÃO 2: RECEBER DO CHATWOOT (Chatwoot -> Wuzapi) ---

func (s *server) HandleChatwootWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Validar Token do Wuzapi
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Token obrigatório", http.StatusUnauthorized)
			return
		}

		// 2. Ler JSON
		var payload CwWebhook
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "JSON inválido", http.StatusBadRequest)
			return
		}

		// 3. Filtrar eventos (apenas mensagens enviadas pelo agente)
		if payload.Event != "message_created" || payload.MessageType != "outgoing" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// 4. Buscar sessão do Wuzapi
		userInfo, found := userinfocache.Get(token)
		if !found {
			fmt.Println("[ChatwootWebhook] Sessão não encontrada para o token")
			http.Error(w, "Sessão inválida", http.StatusUnauthorized)
			return
		}

		// 5. Obter cliente WhatsApp
		vals, ok := userInfo.(Values)
		if !ok {
			fmt.Println("[ChatwootWebhook] Erro de cast no cache")
			http.Error(w, "Erro interno", http.StatusInternalServerError)
			return
		}
		userID := vals.Get("Id")
		client := clientManager.GetWhatsmeowClient(userID)

		if client == nil || !client.IsConnected() {
			fmt.Println("[ChatwootWebhook] WhatsApp desconectado")
			http.Error(w, "WhatsApp offline", http.StatusInternalServerError)
			return
		}

		// 6. Preparar envio
		recipientPhone := payload.Conversation.ContactInbox.SourceID
		// Remover o "+" se vier do Chatwoot, pois o parseJID do Wuzapi geralmente espera números limpos ou trata isso
		// Mas aqui vamos assumir que precisamos limpar
		recipientPhoneClean := strings.Replace(recipientPhone, "+", "", -1)
		
		recipientJID, ok := parseJID(recipientPhoneClean)
		if !ok {
			fmt.Printf("[ChatwootWebhook] Erro ao parsear número: %s\n", recipientPhone)
			return
		}

		// 7. Enviar
		fmt.Printf("[Chatwoot -> WhatsApp] Enviando para %s: %s\n", recipientPhoneClean, payload.Content)
		
		_, err := client.SendMessage(context.Background(), recipientJID, &waE2E.Message{
			Conversation: proto.String(payload.Content),
		})

		if err != nil {
			fmt.Printf("[ChatwootWebhook] Erro ao enviar: %v\n", err)
		}

		w.WriteHeader(http.StatusOK)
	}
}
