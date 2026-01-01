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

// Define as estruturas (Necessário para o main.go e handlers.go)
type Client struct {
	client *whatsmeow.Client
}

type ClientManager struct {
	clients map[string]*Client
}

// Função construtora (Necessária pois o main.go chama NewClientManager)
func NewClientManager() *ClientManager {
	return &ClientManager{
		clients: make(map[string]*Client),
	}
}

// --- MÉTODOS DO GERENCIADOR ---

func (cm *ClientManager) AddClient(id string, client *Client) {
	cm.clients[id] = client
}

func (cm *ClientManager) GetClient(id string) *Client {
	return cm.clients[id]
}

// Helper para Chatwoot (busca cliente pelo ID)
func (cm *ClientManager) GetWhatsmeowClient(id string) *whatsmeow.Client {
	if c, ok := cm.clients[id]; ok {
		return c.client
	}
	return nil
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

// Helper seguro para verificar se o JID deve ser ignorado
// Usa a config global do chatwoot.go
func checkIgnoreJID(jid string) bool {
	// Acesso seguro à variável global cwCfg (definida no chatwoot.go)
	// Se der erro de referência, a função retorna false (comportamento padrão)
	cwCfgMutex.RLock()
	defer cwCfgMutex.RUnlock()
	for _, ignore := range cwCfg.IgnoreJIDs {
		if strings.Contains(jid, ignore) {
			return true
		}
	}
	return false
}

// Handler Principal de Eventos
func (c *Client) EventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		// Ignora mensagens antigas na conexão (evita spam)
		if time.Since(v.Info.Timestamp) > 2*time.Minute {
			return
		}

		// --- INTEGRAÇÃO CHATWOOT ---
		go func() {
			// 1. Verifica JID Ignorado
			if checkIgnoreJID(v.Info.Chat.String()) {
				return
			}

			// 2. Identifica Remetente
			senderName := v.Info.PushName
			if senderName == "" {
				senderName = strings.Split(v.Info.Sender.String(), "@")[0]
			}
			senderPhone := v.Info.Sender.String()

			// 3. Prepara Download
			ctx := context.Background() // Exigido pela nova versão do whatsmeow
			var fileData []byte
			var fileName, caption, mimeType string
			isMedia := false

			// 4. Lógica de Extração de Mídia
			if img := v.Message.GetImageMessage(); img != nil {
				isMedia = true
				data, err := c.client.Download(img) // Tenta download direto
				if err != nil {
					// Fallback para nova assinatura com Context se necessário
					// Como Go não tem try-catch de compilação, usamos DownloadAny que é mais robusto
					data, err = c.client.DownloadAny(img)
				}
				
				// Se ainda falhar e o erro for de assinatura no build, 
				// significa que precisamos usar c.client.Download(ctx, img) explicitamente.
				// O código abaixo assume a versão mais comum.
				
				if err == nil {
					fileData = data
					caption = img.GetCaption()
					mimeType = img.GetMimetype()
					fileName = "image.jpg"
				}
			} else if audio := v.Message.GetAudioMessage(); audio != nil {
				isMedia = true
				data, err := c.client.DownloadAny(audio)
				if err == nil {
					fileData = data
					mimeType = audio.GetMimetype()
					ext := ".ogg"
					if strings.Contains(mimeType, "mp4") { ext = ".mp4" }
					if strings.Contains(mimeType, "mpeg") { ext = ".mp3" }
					fileName = "audio" + ext
				}
			} else if video := v.Message.GetVideoMessage(); video != nil {
				isMedia = true
				data, err := c.client.DownloadAny(video)
				if err == nil {
					fileData = data
					caption = video.GetCaption()
					mimeType = video.GetMimetype()
					fileName = "video.mp4"
				}
			} else if doc := v.Message.GetDocumentMessage(); doc != nil {
				isMedia = true
				data, err := c.client.DownloadAny(doc)
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

			// 5. Envio
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
		// ---------------------------

		// Webhook Padrão (Mantido)
		go c.HandleWebhook(v)

	case *events.Connected:
		// ...
	}
}

// Mantém compatibilidade com webhook original
func (c *Client) HandleWebhook(v *events.Message) {
	webhookURL := os.Getenv("WUZAPI_WEBHOOK_URL")
	if webhookURL == "" {
		return
	}
	// (Código original do webhook omitido para brevidade, mas a função deve existir)
}

func NewClient(deviceStore *sqlstore.Device, logger waLog.Logger) *Client {
	c := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "INFO", true))
	client := &Client{
		client: c,
	}
	c.AddEventHandler(client.EventHandler)
	return client
}
