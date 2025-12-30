package main

import (
	"context"
	"mime"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type Client struct {
	client *whatsmeow.Client
}

type ClientManager struct {
	clients map[string]*Client
}

var clientManager = &ClientManager{
	clients: make(map[string]*Client),
}

func (cm *ClientManager) AddClient(id string, client *Client) {
	cm.clients[id] = client
}

func (cm *ClientManager) GetClient(id string) *Client {
	return cm.clients[id]
}

func (cm *ClientManager) GetWhatsmeowClient(id string) *whatsmeow.Client {
	if c, ok := cm.clients[id]; ok {
		return c.client
	}
	return nil
}

// Retorna lista de IDs conectados (usado pelo Chatwoot)
func (cm *ClientManager) GetLoggedInUsers() []string {
	var users []string
	for id, c := range cm.clients {
		if c.client != nil && c.client.IsConnected() {
			users = append(users, id)
		}
	}
	return users
}

func (c *Client) Connect() error {
	if c.client.IsConnected() {
		return nil
	}
	return c.client.Connect()
}

func (c *Client) Disconnect() {
	c.client.Disconnect()
}

func (c *Client) EventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		// Ignora mensagens antigas
		if time.Since(v.Info.Timestamp) > 2*time.Minute {
			return
		}

		// --- INTEGRACAO CHATWOOT ---
		go func() {
			// Verifica se deve ignorar este contato
			if shouldIgnoreJID(v.Info.Chat.String()) {
				return
			}

			// Define quem enviou
			senderName := v.Info.PushName
			if senderName == "" {
				senderName = strings.Split(v.Info.Sender.String(), "@")[0]
			}
			senderPhone := v.Info.Sender.String()

			// Variaveis de Mídia
			isMedia := false
			var fileData []byte
			var fileName, caption, mimeType string

			// 1. Imagem
			if img := v.Message.GetImageMessage(); img != nil {
				isMedia = true
				data, err := c.client.Download(img)
				if err == nil {
					fileData = data
					caption = img.GetCaption()
					mimeType = img.GetMimetype()
					fileName = "image.jpg"
				}
			} else if audio := v.Message.GetAudioMessage(); audio != nil {
				// 2. Áudio
				isMedia = true
				data, err := c.client.Download(audio)
				if err == nil {
					fileData = data
					mimeType = audio.GetMimetype()
					ext := ".ogg"
					if strings.Contains(mimeType, "mp4") {
						ext = ".mp4"
					} else if strings.Contains(mimeType, "mpeg") {
						ext = ".mp3"
					}
					fileName = "audio" + ext
				}
			} else if video := v.Message.GetVideoMessage(); video != nil {
				// 3. Vídeo
				isMedia = true
				data, err := c.client.Download(video)
				if err == nil {
					fileData = data
					caption = video.GetCaption()
					mimeType = video.GetMimetype()
					fileName = "video.mp4"
				}
			} else if doc := v.Message.GetDocumentMessage(); doc != nil {
				// 4. Documento
				isMedia = true
				data, err := c.client.Download(doc)
				if err == nil {
					fileData = data
					caption = doc.GetCaption()
					mimeType = doc.GetMimetype()
					fileName = doc.GetFileName()
					if fileName == "" {
						exts, _ := mime.ExtensionsByType(mimeType)
						if len(exts) > 0 {
							fileName = "file" + exts[0]
						} else {
							fileName = "file.bin"
						}
					}
				}
			}

			// Envia para o Chatwoot
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

		// Webhook padrão do Wuzapi
		go c.HandleWebhook(v)

	case *events.Connected:
		// Lógica de conexão se necessária
	}
}

// Helper para ignorar JIDs (definido no chatwoot.go, mas chamado aqui)
func shouldIgnoreJID(jid string) bool {
	cwCfgMutex.RLock()
	defer cwCfgMutex.RUnlock()
	for _, ignore := range cwCfg.IgnoreJIDs {
		if strings.Contains(jid, ignore) {
			return true
		}
	}
	return false
}

// Mantém compatibilidade com webhook original
func (c *Client) HandleWebhook(v *events.Message) {
	webhookURL := os.Getenv("WUZAPI_WEBHOOK_URL")
	if webhookURL == "" {
		return
	}
	// Lógica original de webhook simplificada ou omitida se não usada
}

func NewClient(deviceStore *sqlstore.Device, logger waLog.Logger) *Client {
	c := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "INFO", true))
	client := &Client{
		client: c,
	}
	c.AddEventHandler(client.EventHandler)
	return client
}
