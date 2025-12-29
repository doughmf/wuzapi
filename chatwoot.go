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

// --- ENVIAR PARA CHATWOOT (Wuzapi -> Chatwoot) ---

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
	// Remove o + se já vier com ele para garantir limpeza, depois adiciona
	phoneClean := strings.Replace(senderUser, "+", "", -1)
	phoneNumber := "+" + phoneClean

	// 1. Busca ou Cria o Contato (Obrigatório para Chatwoot 4.9+)
	contactID := getOrCreateContact(cwURL, cwAccountID, cwToken, cwInboxID, phoneNumber, pushName)
	if contactID == 0 {
		fmt.Println("[Chatwoot] Falha ao obter ID do contato.")
		return
	}

	// 2. Envia a mensagem
	sendConversation(cwURL, cwAccountID, cwToken, cwInboxID, contactID, text)
}

func getOrCreateContact(baseURL, accountID, token string, inboxID int, phone, name string) int {
	// Tenta buscar
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

	// Se não achou, cria
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
		fmt.Printf("[Chatwoot] Erro criando contato: %v\n", err)
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
		fmt.Printf("[Chatwoot] Mensagem enviada para contato %d\n", contactID)
	} else {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("[Chatwoot] Erro %d: %s\n", resp.StatusCode, string(body))
	}
}

// --- RECEBER DO CHATWOOT (Chatwoot -> Wuzapi) ---

func (s *server) HandleChatwootWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Token obrigatório", http.StatusUnauthorized)
			return
		}

		var payload CwWebhook
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return
		}

		// Filtra apenas mensagens enviadas pelo agente (outgoing) e que sejam texto criado
		if payload.Event != "message_created" || payload.MessageType != "outgoing" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Busca a sessão do usuário
		userInfo, found := userinfocache.Get(token)
		if !found {
			fmt.Println("[Webhook] Token inválido ou sessão não encontrada")
			http.Error(w, "Sessão inválida", http.StatusUnauthorized)
			return
		}
		
		vals, ok := userInfo.(Values)
		if !ok { return }
		userID := vals.Get("Id")
		client := clientManager.GetWhatsmeowClient(userID)

		if client == nil || !client.IsConnected() {
			fmt.Println("[Webhook] WhatsApp desconectado")
			return
		}

		// Envia a mensagem
		phone := strings.Replace(payload.Conversation.ContactInbox.SourceID, "+", "", -1)
		jid, _ := parseJID(phone)
		
		fmt.Printf("[Chatwoot -> WhatsApp] Enviando para %s: %s\n", phone, payload.Content)
		client.SendMessage(context.Background(), jid, &waE2E.Message{
			Conversation: proto.String(payload.Content),
		})

		w.WriteHeader(http.StatusOK)
	}
}
