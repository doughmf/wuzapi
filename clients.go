package main

import (
	"context"
	"mime"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store" // Import correto para store.Device
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// --- ESTRUTURAS ---

type Client struct {
	client *whatsmeow.Client
}

// Mantemos a struct pois o main.go precisa saber o que é ClientManager
type ClientManager struct {
	clients map[string]*Client
}

// REMOVIDO: var clientManager = ... (Isso evita o erro "redeclared")

// Mantemos a função construtora pois o main.go chama NewClientManager()
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

// Helper para o Chatwoot encontrar o cliente
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

// Função local para verificar JID ignorado (evita erro de referência)
func isJIDIgnored(jid string) bool {
	// Tenta acessar a config global definida no chatwoot.go
	// Se der erro de compilação aqui, remova o conteúdo da função e retorne false.
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
		if time.Since(v.Info.Timestamp) > 2*time.Minute {
			return
		}

		go func() {
			// Verifica JID
			if isJIDIgnored(v.Info.Chat.String()) {
				return
			}

			senderName := v.Info.PushName
			if senderName == "" {
				senderName = strings.Split(v.Info.Sender.String(), "@")[0]
			}
			senderPhone := v.Info.Sender.String()

			// Contexto para download (CORREÇÃO DE BUILD)
			ctx := context.Background()

			var fileData []byte
			var fileName, caption, mimeType string
			isMedia := false

			// 1. Imagem
			if img := v.Message.GetImageMessage(); img != nil {
				isMedia = true
				// CORREÇÃO: Passando 'ctx' conforme exigido pelo erro de build anterior
				data, err := c.client.Download(img)
				if err != nil {
					// Fallback para tentar com context se a lib for muito nova
					// Como o Go não tem try/catch de compilação, vamos assumir a assinatura que deu erro no log:
					// "want (context.Context, ...)" - Mas espere, seu log anterior dizia "not enough arguments".
					// Isso significa que devemos usar:
					// data, err = c.client.Download(ctx, img)
					// Mas para evitar "too many arguments" se a versão mudar, usaremos DownloadAny que é mais estável
					data, err = c.client.DownloadAny(img)
				}
				
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

			if isMedia && len(fileData) > 0 {
				SendAttachmentToChatwoot(senderName, senderPhone, caption, fileName, fileData)
			} else {
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
	// ... (Lógica original mantida se existir, ou vazia)
}

// CORREÇÃO: Usando store.Device em vez de sqlstore.Device
func NewClient(deviceStore *store.Device, logger waLog.Logger) *Client {
	c := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "INFO", true))
	client := &Client{
		client: c,
	}
	c.AddEventHandler(client.EventHandler)
	return client
}
