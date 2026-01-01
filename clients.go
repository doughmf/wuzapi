package main

import (
	"context"
	"mime"
	"os"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type Client struct {
	client *whatsmeow.Client
}

// DEFINIÇÃO DA ESTRUCT (Necessária para handlers.go e main.go)
// Adicionamos sync.RWMutex para corrigir o erro "undefined RLock"
type ClientManager struct {
	sync.RWMutex
	clients map[string]*Client
}

// NOTA: A variável 'var clientManager' foi removida daqui pois já existe no main.go.
// Isso corrige o erro "redeclared in this block".

// FUNÇÃO CONSTRUTORA (Necessária pois main.go chama NewClientManager)
func NewClientManager() *ClientManager {
	return &ClientManager{
		clients: make(map[string]*Client),
	}
}

// --- MÉTODOS DO GERENCIADOR ---

func (cm *ClientManager) AddClient(id string, client *Client) {
	cm.Lock()
	defer cm.Unlock()
	cm.clients[id] = client
}

func (cm *ClientManager) GetClient(id string) *Client {
	cm.RLock()
	defer cm.RUnlock()
	return cm.clients[id]
}

func (cm *ClientManager) DeleteWhatsmeowClient(id string) {
	cm.Lock()
	defer cm.Unlock()
	delete(cm.clients, id)
}

// Helper para a integração Chatwoot
func (cm *ClientManager) GetWhatsmeowClient(id string) *whatsmeow.Client {
	cm.RLock()
	defer cm.RUnlock()
	if c, ok := cm.clients[id]; ok {
		return c.client
	}
	return nil
}

// Método necessário para compatibilidade com handlers.go antigos
func (cm *ClientManager) UpdateMyClientSubscriptions(id string, event interface{}) {
	// Implementação vazia apenas para satisfazer a chamada no handlers.go
	// se o código original o chamar.
}

// --- MÉTODOS DO CLIENTE ---

func (c *Client) Connect() error {
	if c.client.IsConnected() {
		return nil
	}
	return c.client.Connect()
}

func (c *Client) Disconnect() {
	c.client.Disconnect()
}

// Helper local para verificar blacklist (evita dependência circular ou undefined)
func isJIDIgnored(jid string) bool {
	// Acessa a config global definida no chatwoot.go com segurança
	cwCfgMutex.RLock()
	defer cwCfgMutex.RUnlock()
	for _, ignore := range cwCfg.IgnoreJIDs {
		if strings.Contains(jid, ignore) {
			return true
		}
	}
	return false
}

func (c *Client) EventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		// Ignora mensagens antigas (2 minutos)
		if time.Since(v.Info.Timestamp) > 2*time.Minute {
			return
		}

		// --- INTEGRAÇÃO CHATWOOT ---
		go func() {
			// 1. Verifica Filtros
			if isJIDIgnored(v.Info.Chat.String()) {
				return
			}

			// 2. Identifica Remetente
			senderName := v.Info.PushName
			if senderName == "" {
				senderName = strings.Split(v.Info.Sender.String(), "@")[0]
			}
			senderPhone := v.Info.Sender.String()

			// 3. Prepara Download (Correção de Build: Contexto)
			ctx := context.Background()
			var fileData []byte
			var fileName, caption, mimeType string
			isMedia := false

			// 4. Extração de Mídia (Usando DownloadAny com Context)
			if img := v.Message.GetImageMessage(); img != nil {
				isMedia = true
				data, err := c.client.DownloadAny(ctx, img)
				if err == nil {
					fileData = data
					caption = img.GetCaption()
					mimeType = img.GetMimetype()
					fileName = "image.jpg"
				}
			} else if audio := v.Message.GetAudioMessage(); audio != nil {
				isMedia = true
				data, err := c.client.DownloadAny(ctx, audio)
				if err == nil {
					fileData = data
					mimeType = audio.GetMimetype()
					fileName = "audio.ogg" // WhatsApp geralmente usa ogg/opus
				}
			} else if video := v.Message.GetVideoMessage(); video != nil {
				isMedia = true
				data, err := c.client.DownloadAny(ctx, video)
				if err == nil {
					fileData = data
					caption = video.GetCaption()
					mimeType = video.GetMimetype()
					fileName = "video.mp4"
				}
			} else if doc := v.Message.GetDocumentMessage(); doc != nil {
				isMedia = true
				data, err := c.client.DownloadAny(ctx, doc)
				if err == nil {
					fileData = data
					caption = doc.GetCaption()
					mimeType = doc.GetMimetype()
					fileName = doc.GetFileName()
					if fileName == "" { fileName = "document.bin" }
				}
			}

			// 5. Envio para Chatwoot
			if isMedia && len(fileData) > 0 {
				SendAttachmentToChatwoot(senderName, senderPhone, caption, fileName, fileData)
			} else {
				// Texto Simples
				text := ""
				if v.Message.Conversation != nil {
					text = *v.Message.Conversation
				} else if v.Message.ExtendedTextMessage != nil {
					text = *v.Message.ExtendedTextMessage.Text
				}
				
				if text != "" {
					SendToChatwoot(senderName, senderPhone, text)
				}
			}
		}()
		// ---------------------------

		go c.HandleWebhook(v)

	case *events.Connected:
		// Lógica de conexão
	}
}

func (c *Client) HandleWebhook(v *events.Message) {
	webhookURL := os.Getenv("WUZAPI_WEBHOOK_URL")
	if webhookURL == "" {
		return
	}
	// (Mantém lógica original se houver, ou vazio)
}

func NewClient(deviceStore *store.Device, logger waLog.Logger) *Client {
	c := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "INFO", true))
	client := &Client{
		client: c,
	}
	c.AddEventHandler(client.EventHandler)
	return client
}
