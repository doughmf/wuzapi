package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// --- CONFIGURAÇÃO ---
type ChatwootConfig struct {
	Enabled             bool     `json:"enabled"`
	URL                 string   `json:"url"`
	Token               string   `json:"token"`
	AccountID           string   `json:"account_id"`
	InboxID             string   `json:"inbox_id"`
	SignMessages        bool     `json:"sign_messages"`
	SignatureDelimiter  string   `json:"signature_delimiter"`
	ReopenConversation  bool     `json:"reopen_conversation"`
	ConversationPending bool     `json:"conversation_pending"`
	InboxName           string   `json:"inbox_name"`
	Organization        string   `json:"organization"`
	LogoURL             string   `json:"logo_url"`
	ImportContacts      bool     `json:"import_contacts"`
	ImportMessages      bool     `json:"import_messages"`
	DaysLimit           int      `json:"days_limit"`
	IgnoreJIDs          []string `json:"ignore_jids"`
}

var (
	cwCfg      ChatwootConfig
	cwCfgMutex sync.RWMutex
)

const configFile = "chatwoot.json"

func init() {
	loadConfig()
	// Ignora erro de certificado SSL para downloads de mídia
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
}

func loadConfig() {
	cwCfgMutex.Lock()
	defer cwCfgMutex.Unlock()

	cwCfg = ChatwootConfig{
		SignatureDelimiter: "\n",
		DaysLimit:          7,
	}

	file, err := os.Open(configFile)
	if err == nil {
		defer file.Close()
		json.NewDecoder(file).Decode(&cwCfg)
		return
	}

	cwCfg.URL = strings.TrimSpace(os.Getenv("CHATWOOT_URL"))
	cwCfg.Token = strings.TrimSpace(os.Getenv("CHATWOOT_TOKEN"))
	cwCfg.AccountID = strings.TrimSpace(os.Getenv("CHATWOOT_ACCOUNT_ID"))
	cwCfg.InboxID = strings.TrimSpace(os.Getenv("CHATWOOT_INBOX_ID"))
	cwCfg.Enabled = cwCfg.URL != ""
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

type CreateInboxChannel struct {
	Type       string `json:"type"`
	WebhookUrl string `json:"webhook_url"`
}

type CreateInboxRequest struct {
	Name                       string             `json:"name"`
	Channel                    CreateInboxChannel `json:"channel"`
	AllowMessagesAfterResolved bool               `json:"allow_messages_after_resolved"`
	EnableAutoAssignment       bool               `json:"enable_auto_assignment"`
}

type CreateInboxResponse struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
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

type CwAttachment struct {
	ID          int    `json:"id"`
	MessageType string `json:"message_type"`
	FileType    string `json:"file_type"`
	DataUrl     string `json:"data_url"`
	ThumbUrl    string `json:"thumb_url"`
}

type CwWebhook struct {
	Event        string         `json:"event"`
	MessageType  string         `json:"message_type"`
	Content      string         `json:"content"`
	Attachments  []CwAttachment `json:"attachments"`
	Sender       struct {
		Name string `json:"name"`
	} `json:"sender"`
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

func sendJsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// --- API HANDLERS ---

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

// --- AUTO CRIAÇÃO ---
func (s *server) HandleAutoCreateInbox() http.HandlerFunc {
	type Wrapper struct {
		Config       ChatwootConfig `json:"config"`
		SessionToken string         `json:"session_token"`
		WuzapiURL    string         `json:"wuzapi_url"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var body Wrapper
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			sendJsonError(w, "JSON inválido", http.StatusBadRequest)
			return
		}

		if r.Header.Get("Authorization") != os.Getenv("WUZAPI_ADMIN_TOKEN") {
			sendJsonError(w, "Token de Admin inválido", http.StatusUnauthorized)
			return
		}

		if body.SessionToken == "" {
			sendJsonError(w, "Instance Token obrigatório", http.StatusBadRequest)
			return
		}

		cfg := body.Config
		cfg.URL = strings.TrimSuffix(cfg.URL, "/")
		
		webhookEndpoint := fmt.Sprintf("%s/chatwoot/webhook?token=%s", body.WuzapiURL, body.SessionToken)

		cwPayload := CreateInboxRequest{
			Name: cfg.InboxName,
			Channel: CreateInboxChannel{
				Type:       "api",
				WebhookUrl: webhookEndpoint,
			},
			AllowMessagesAfterResolved: cfg.ReopenConversation,
			EnableAutoAssignment:       !cfg.ConversationPending,
		}
		jsonPayload, _ := json.Marshal(cwPayload)

		targetURL := fmt.Sprintf("%s/api/v1/accounts/%s/inboxes", cfg.URL, cfg.AccountID)
		cwReq, _ := http.NewRequest("POST", targetURL, bytes.NewBuffer(jsonPayload))
		cwReq.Header.Set("Content-Type", "application/json")
		cwReq.Header.Set("api_access_token", cfg.Token)

		client := &http.Client{}
		resp, err := client.Do(cwReq)
		if err != nil {
			sendJsonError(w, "Erro conexão: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			sendJsonError(w, fmt.Sprintf("Erro Chatwoot (%d): %s", resp.StatusCode, string(bodyBytes)), resp.StatusCode)
			return
		}

		var cwResp CreateInboxResponse
		if err := json.NewDecoder(resp.Body).Decode(&cwResp); err != nil {
			sendJsonError(w, "Erro leitura resposta", http.StatusInternalServerError)
			return
		}

		cfg.InboxID = strconv.Itoa(cwResp.Id)
		saveConfigToDisk(cfg)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "success",
			"inbox_id": cwResp.Id,
			"message":  "Caixa configurada com sucesso!",
		})
	}
}

// --- LÓGICA DE CONTATO ---

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
		"source_id":    phone,
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
		var contactRes ChatwootContactResponse
		json.NewDecoder(respCreate.Body).Decode(&contactRes)
		return contactRes.Payload.Contact.ID
	}
	return 0
}

// --- ENVIO: WHATSAPP -> CHATWOOT ---

func SendToChatwoot(pushName string, senderUser string, text string) {
	cwCfgMutex.RLock()
	cfg := cwCfg
	cwCfgMutex.RUnlock()

	if !cfg.Enabled || cfg.URL == "" || cfg.Token == "" {
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

	url := fmt.Sprintf("%s/api/v1/accounts/%s/conversations", cfg.URL, cfg.AccountID)
	payload := map[string]interface{}{
		"inbox_id":     cwInboxID,
		"contact_id":   contactID,
		"status":       "open",
		"message": map[string]string{
			"content":      text,
			"message_type": "incoming",
		},
	}
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", cfg.Token)
	client := &http.Client{}
	resp, _ := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
}

func SendAttachmentToChatwoot(pushName, senderUser, caption, fileName string, fileData []byte) {
	cwCfgMutex.RLock()
	cfg := cwCfg
	cwCfgMutex.RUnlock()

	if !cfg.Enabled || cfg.URL == "" || cfg.Token == "" {
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

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, _ := writer.CreateFormFile("attachments[]", fileName)
	part.Write(fileData)

	writer.WriteField("content", caption)
	writer.WriteField("message_type", "incoming")
	writer.WriteField("inbox_id", cfg.InboxID)
	writer.WriteField("contact_id", strconv.Itoa(contactID))
	writer.Close()

	url := fmt.Sprintf("%s/api/v1/accounts/%s/conversations", cfg.URL, cfg.AccountID)
	req, _ := http.NewRequest("POST", url, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("api_access_token", cfg.Token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
	}
}

// --- WEBHOOK: CHATWOOT -> WHATSAPP ---

func (s *server) HandleChatwootWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			w.WriteHeader(http.StatusOK)
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

		cwCfgMutex.RLock()
		cfg := cwCfg
		cwCfgMutex.RUnlock()

		userInfo, found := userinfocache.Get(token)
		if !found {
			fmt.Printf("[Chatwoot] Erro: Sessão não encontrada para o token %s\n", token)
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusOK)

		go func() {
			vals, ok := userInfo.(Values)
			if !ok {
				return
			}
			userID := vals.Get("Id")
			client := clientManager.GetWhatsmeowClient(userID)
			if client == nil || !client.IsConnected() {
				return
			}

			phone := payload.Conversation.Contact.PhoneNumber
			if phone == "" {
				phone = payload.Conversation.ContactInbox.SourceID
			}
			phone = strings.ReplaceAll(phone, "+", "")
			phone = strings.ReplaceAll(phone, " ", "")
			if len(phone) < 8 {
				return
			}
			
			jid, err := types.ParseJID(phone)
			if err != nil {
				jid, err = types.ParseJID(phone + "@s.whatsapp.net")
				if err != nil {
					fmt.Println("[Chatwoot] Erro ao parsear JID:", err)
					return
				}
			}

			if len(payload.Attachments) > 0 {
				for _, att := range payload.Attachments {
					sendChatwootMedia(client, jid, att)
				}
			} else {
				finalMessage := payload.Content
				if cfg.SignMessages && payload.Sender.Name != "" {
					delimiter := strings.ReplaceAll(cfg.SignatureDelimiter, `\n`, "\n")
					finalMessage = fmt.Sprintf("%s%s%s", finalMessage, delimiter, payload.Sender.Name)
				}
				if finalMessage != "" {
					client.SendMessage(context.Background(), jid, &waE2E.Message{Conversation: proto.String(finalMessage)})
				}
			}
		}()
	}
}

func sendChatwootMedia(client *whatsmeow.Client, jid types.JID, att CwAttachment) {
	resp, err := http.Get(att.DataUrl)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	uploadResp, err := client.Upload(context.Background(), data, whatsmeow.MediaImage)
	if err != nil {
		return
	}

	switch att.FileType {
	case "image":
		msg := &waE2E.ImageMessage{
			URL:           proto.String(uploadResp.URL),
			DirectPath:    proto.String(uploadResp.DirectPath),
			MediaKey:      uploadResp.MediaKey,
			Mimetype:      proto.String(http.DetectContentType(data)),
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
		}
		client.SendMessage(context.Background(), jid, &waE2E.Message{ImageMessage: msg})
	case "audio":
		msg := &waE2E.AudioMessage{
			URL:           proto.String(uploadResp.URL),
			DirectPath:    proto.String(uploadResp.DirectPath),
			MediaKey:      uploadResp.MediaKey,
			Mimetype:      proto.String("audio/ogg; codecs=opus"),
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			PTT:           proto.Bool(true),
		}
		client.SendMessage(context.Background(), jid, &waE2E.Message{AudioMessage: msg})
	default:
		msg := &waE2E.DocumentMessage{
			URL:           proto.String(uploadResp.URL),
			DirectPath:    proto.String(uploadResp.DirectPath),
			MediaKey:      uploadResp.MediaKey,
			Mimetype:      proto.String(http.DetectContentType(data)),
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			FileName:      proto.String("arquivo"),
		}
		client.SendMessage(context.Background(), jid, &waE2E.Message{DocumentMessage: msg})
	}
}
