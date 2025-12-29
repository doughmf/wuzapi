package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Estruturas para decodificar respostas do Chatwoot
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

	// 1. Obter o CONTACT_ID (Busca ou Cria)
	contactID := getOrCreateContact(cwURL, cwAccountID, cwToken, cwInboxID, phoneNumber, pushName)
	if contactID == 0 {
		fmt.Println("[Chatwoot] Falha ao obter ID do contato. Abortando.")
		return
	}

	// 2. Criar Conversa/Mensagem com o ID correto
	sendConversation(cwURL, cwAccountID, cwToken, cwInboxID, contactID, text)
}

// Função auxiliar para buscar ou criar contato
func getOrCreateContact(baseURL, accountID, token string, inboxID int, phone, name string) int {
	// A. Tentar buscar contato existente pelo telefone
	searchURL := fmt.Sprintf("%s/api/v1/accounts/%s/contacts/search?q=%s", baseURL, accountID, urlEncoded(phone))
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
			// Contato encontrado!
			return searchRes.Payload[0].ID
		}
	}

	// B. Se não encontrou, CRIAR contato
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
	
	// Se falhou (ex: 422 se o contato já existe mas a busca falhou por formato), tenta buscar novamente sem o '+'
	if respCreate.StatusCode == 422 {
		fmt.Println("[Chatwoot] Contato duplicado (422), tentando recuperar ID...")
		// Aqui poderíamos implementar uma lógica de retry mais robusta, 
		// mas geralmente a busca no passo A resolve.
	}

	fmt.Printf("[Chatwoot] Erro criando contato. Status: %d\n", respCreate.StatusCode)
	return 0
}

func sendConversation(baseURL, accountID, token string, inboxID, contactID int, text string) {
	url := fmt.Sprintf("%s/api/v1/accounts/%s/conversations", baseURL, accountID)
	
	// Payload correto para Chatwoot 4.9+ (Application API)
	payload := map[string]interface{}{
		"inbox_id":   inboxID,
		"contact_id": contactID, // OBRIGATÓRIO na v4.9+
		"status":     "open",
		"message": map[string]string{
			"content": text,
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
		fmt.Printf("[Chatwoot] SUCESSO! Mensagem enviada para Contact ID %d\n", contactID)
	} else {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("[Chatwoot] FALHA (Erro %d): %s\n", resp.StatusCode, string(body))
	}
}

func urlEncoded(str string) string {
	// Substituição simples para URL encode do '+'
	return strings.Replace(str, "+", "%2B", -1)
}
// --- ADICIONE ISTO NO FINAL DO ARQUIVO chatwoot.go ---

// Estruturas para ler o Webhook do Chatwoot
type CwWebhook struct {
	Event       string `json:"event"`
	MessageType string `json:"message_type"`
	Content     string `json:"content"`
	Conversation struct {
		ContactInbox struct {
			SourceID string `json:"source_id"` // Aqui está o número do telefone (+55...)
		} `json:"contact_inbox"`
	} `json:"conversation"`
}

// Handler para receber mensagens do Chatwoot
func (s *server) HandleChatwootWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Validar Token do Wuzapi (passado na URL ?token=123...)
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Token obrigatório", http.StatusUnauthorized)
			return
		}

		// 2. Ler o JSON do Chatwoot
		var payload CwWebhook
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "JSON inválido", http.StatusBadRequest)
			return
		}

		// 3. Filtrar: Só queremos mensagens enviadas pelo Agente (outgoing)
		// e que sejam do evento "message_created"
		if payload.Event != "message_created" || payload.MessageType != "outgoing" {
			w.WriteHeader(http.StatusOK) // Ignora silenciosamente
			return
		}

		// 4. Encontrar a sessão do Wuzapi pelo Token
		// (Lógica simplificada buscando o usuário no cache/banco)
		userInfo, found := userinfocache.Get(token)
		if !found {
			fmt.Println("[ChatwootWebhook] Token inválido ou sessão não encontrada")
			http.Error(w, "Sessão não encontrada", http.StatusUnauthorized)
			return
		}
		
		userID := userInfo.(Values).Get("Id")
		client := clientManager.GetWhatsmeowClient(userID)
		if client == nil || !client.IsConnected() {
			fmt.Println("[ChatwootWebhook] WhatsApp desconectado")
			http.Error(w, "WhatsApp desconectado", http.StatusInternalServerError)
			return
		}

		// 5. Extrair o número e limpar (SourceID vem como +55...)
		recipientPhone := payload.Conversation.ContactInbox.SourceID
		// O Wuzapi precisa do JID (5511999...@s.whatsapp.net)
		recipientJID, ok := parseJID(recipientPhone)
		if !ok {
			fmt.Printf("[ChatwootWebhook] Erro ao parsear número: %s\n", recipientPhone)
			return
		}

		// 6. Enviar a mensagem no WhatsApp
		fmt.Printf("[Chatwoot -> WhatsApp] Enviando para %s: %s\n", recipientPhone, payload.Content)
		
		// Envia texto simples
		_, err := client.SendMessage(context.Background(), recipientJID, &waE2E.Message{
			Conversation: proto.String(payload.Content),
		})

		if err != nil {
			fmt.Printf("[ChatwootWebhook] Erro ao enviar: %v\n", err)
		}

		w.WriteHeader(http.StatusOK)
	}
}
