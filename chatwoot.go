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
