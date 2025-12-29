package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

func SendToChatwoot(pushName string, senderUser string, text string) {
	cwURL := os.Getenv("CHATWOOT_URL")
	cwToken := os.Getenv("CHATWOOT_TOKEN")
	cwAccountID := os.Getenv("CHATWOOT_ACCOUNT_ID")
	cwInboxID := os.Getenv("CHATWOOT_INBOX_ID")

	if cwURL == "" || cwToken == "" {
		fmt.Println("[Chatwoot] Integração não configurada (falta URL ou Token)")
		return
	}

	// 1. Formatar o número (Source ID)
	// O whatsmeow entrega o user (ex: 551199999999). Adicionamos o + para o Chatwoot.
	phoneNumber := "+" + senderUser
	
	// 2. Montar o Payload Inteligente (Cria contato + Conversa + Mensagem tudo junto)
	// Documentação: https://www.chatwoot.com/developers/api/#operation/newConversation
	payload := map[string]interface{}{
		"source_id": phoneNumber,
		"inbox_id":  cwInboxID,
		"contact_identifier": phoneNumber,
		"sender": map[string]string{
			"name": pushName, // Atualiza o nome do contato se não existir
		},
		"message": map[string]string{
			"content": text, // O conteúdo da mensagem
		},
	}
	
	jsonPayload, _ := json.Marshal(payload)
	
	// Rota mágica para criar conversa/mensagem em canais API
	url := fmt.Sprintf("%s/api/v1/accounts/%s/conversations", cwURL, cwAccountID)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", cwToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("[Chatwoot] Erro de conexão:", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Printf("[Chatwoot] Mensagem enviada com sucesso: %s\n", text)
	} else {
		fmt.Printf("[Chatwoot] Erro API %d enviando mensagem.\n", resp.StatusCode)
	}
}
