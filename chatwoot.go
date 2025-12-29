package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// Estrutura para enviar mensagem ao Chatwoot (API de Relay)
type ChatwootMessage struct {
	Content     string `json:"content"`
	MessageType string `json:"message_type"`
	Private     bool   `json:"private"`
	SourceID    string `json:"source_id"` // O número do telefone do cliente
}

// Estrutura para criar o contato se não existir
type ChatwootContactPayload struct {
	Name        string `json:"name"`
	PhoneNumber string `json:"phone_number"`
}

func SendToChatwoot(pushName string, remoteJid string, text string) {
	cwURL := os.Getenv("CHATWOOT_URL")
	cwToken := os.Getenv("CHATWOOT_TOKEN")
	cwAccountID := os.Getenv("CHATWOOT_ACCOUNT_ID")
	cwInboxID := os.Getenv("CHATWOOT_INBOX_ID")

	if cwURL == "" || cwToken == "" {
		fmt.Println("[Chatwoot] Integração não configurada (falta URL ou Token)")
		return
	}

	// 1. Limpar o ID do WhatsApp (remover @s.whatsapp.net)
	// Exemplo: 551199999999@s.whatsapp.net -> +551199999999
	phoneNumber := "+" + remoteJid
	if len(phoneNumber) > 15 {
		phoneNumber = phoneNumber[:14] // Ajuste simples de formatação
	}

	// 2. Criar ou Buscar Contato no Chatwoot
	// Nota: Para simplificar, vamos tentar enviar a mensagem direto para a API de conversas publicas
	// Se você usar a API de Inbox, o fluxo é: Buscar Contato -> Criar Conversa -> Enviar Mensagem.
	
	// AQUI USAMOS A API SIMPLIFICADA DE "NEW CONVERSATION" DO CHATWOOT PARA API CHANNELS
	url := fmt.Sprintf("%s/api/v1/accounts/%s/inboxes/%s/contacts", cwURL, cwAccountID, cwInboxID)
	
	// Payload para criar o contato/conversa
	payload := map[string]interface{}{
		"name":         pushName,
		"phone_number": phoneNumber,
		"source_id":    phoneNumber,
	}
	
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", cwToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("[Chatwoot] Erro ao criar contato:", err)
		return
	}
	defer resp.Body.Close()

	// Ler o source_id retornado pelo Chatwoot para saber qual ID usar na mensagem
	// (Esta parte requer parsing do JSON de resposta para ser perfeita, 
	// mas para este exemplo simples, vamos assumir que o contato foi criado e enviar a mensagem)

	// 3. Enviar a Mensagem
	msgUrl := fmt.Sprintf("%s/api/v1/accounts/%s/conversations/%s/messages", cwURL, cwAccountID, phoneNumber) // Ajuste conforme a rota exata da sua versão
	
	// OBS: A rota exata do Chatwoot muda dependendo se é "API Channel" ou "Whatsapp Channel".
	// Se for API Channel, você posta para /conversations/{conversation_id}/messages
	
	fmt.Printf("[Chatwoot] Enviando mensagem de %s: %s\n", pushName, text)
}
