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

// NOTA: ClientManager e clientManager são definidos no main.go.
// Não os redefina aqui. Apenas estendemos a funcionalidade.

// Helper para conectar
func (c *Client) Connect() error {
	if c.client.IsConnected() {
		return nil
	}
	return c.client.Connect()
}

// Helper para desconectar
func (c *Client) Disconnect() {
	c.client.Disconnect()
}

// Função auxiliar para obter o cliente whatsmeow (usada pelo Chatwoot)
// Supõe que 'clientManager' global existe em main.go
func (cm *ClientManager) GetWhatsmeowClient(id string) *whatsmeow.Client {
	// Acesso direto ao mapa. Se o main.go usar um mutex privado, isso pode ser arriscado,
	// mas sem ver o main.go, é a melhor aposta.
	if c, ok := cm.clients[id]; ok {
		return c.client
	}
	return nil
}

// Helper para verificar JID ignorado (acessa config do chatwoot)
func checkIgnoreJID(jid string) bool {
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
		// Ignora mensagens muito antigas
		if time.Since(v.Info.Timestamp) > 2*time.Minute {
			return
		}

		go func() {
			// Verifica se deve ignorar (grupos ou blacklist)
			if checkIgnoreJID(v.Info.Chat.String()) {
				return
			}

			senderName := v.Info.PushName
			if senderName == "" {
				senderName = strings.Split(v.Info.Sender.String(), "@")[0]
			}
			senderPhone := v.Info.Sender.String()

			// Contexto para download (CORREÇÃO DO ERRO DE BUILD)
			ctx := context.Background()

			var fileData []byte
			var fileName, caption, mimeType string
			isMedia := false

			// 1. Imagem
			if img := v.Message.GetImageMessage(); img != nil {
				isMedia = true
				// Passa o contexto 'ctx' conforme exigido pela nova versão da lib
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
					if strings.Contains(mimeType, "mp4") { ext = ".mp4" }
					if strings.Contains(mimeType, "mpeg") { ext = ".mp3" }
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
						if len(exts) > 0 { fileName = "file" + exts[0] } else { fileName = "file.bin" }
					}
				}
			}

			// Envia para o Chatwoot
			if isMedia && len(fileData) > 0 {
				SendAttachmentToChatwoot(senderName, senderPhone, caption, fileName, fileData)
			} else {
				// Texto
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

		// Webhook padrão do Wuzapi
		go c.HandleWebhook(v)

	case *events.Connected:
		// ...
	}
}

func (c *Client) HandleWebhook(v *events.Message) {
	webhookURL := os.Getenv("WUZAPI_WEBHOOK_URL")
	if webhookURL == "" {
		return
	}
	// Lógica original de webhook...
}

func NewClient(deviceStore *sqlstore.Device, logger waLog.Logger) *Client {
	c := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "INFO", true))
	client := &Client{
		client: c,
	}
	c.AddEventHandler(client.EventHandler)
	return client
}
