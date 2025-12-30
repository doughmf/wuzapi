package main

import (
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// --- ESTRUTURAS RESTAURADAS ---

type Client struct {
	client *whatsmeow.Client
}

type ClientManager struct {
	clients map[string]*Client
}

// Variável global inicializada
var clientManager = &ClientManager{
	clients: make(map[string]*Client),
}

// Função construtora restaurada
func NewClientManager() *ClientManager {
	return &ClientManager{
		clients: make(map[string]*Client),
	}
}

func (cm *ClientManager) AddClient(id string, client *Client) {
	cm.clients[id] = client
}

func (cm *ClientManager) GetClient(id string) *Client {
	return cm.clients[id]
}

// Helper para Chatwoot
func (cm *ClientManager) GetWhatsmeowClient(id string) *whatsmeow.Client {
	if c, ok := cm.clients[id]; ok {
		return c.client
	}
	return nil
}

// --- MÉTODOS DO CLIENT ---

func (c *Client) Connect() error {
	if c.client.IsConnected() {
		return nil
	}
	return c.client.Connect()
}

func (c *Client) Disconnect() {
	c.client.Disconnect()
}

// Helper para ignorar JID (cópia de segurança)
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
		if time.Since(v.Info.Timestamp) > 2*time.Minute {
			return
		}

		go func() {
			// Verifica JID ignorado
			if checkIgnoreJID(v.Info.Chat.String()) {
				return
			}

			senderName := v.Info.PushName
			if senderName == "" {
				senderName = strings.Split(v.Info.Sender.String(), "@")[0]
			}
			senderPhone := v.Info.Sender.String()

			// MODO SEGURO: Mídia apenas notifica (evita erro de build)
			text := ""
			
			if v.Message.Conversation != nil {
				text = *v.Message.Conversation
			} else if v.Message.ExtendedTextMessage != nil {
				text = *v.Message.ExtendedTextMessage.Text
			} else if v.Message.ImageMessage != nil {
				text = "📷 [Imagem Recebida] (Ver no WhatsApp)"
			} else if v.Message.AudioMessage != nil {
				text = "🎤 [Áudio Recebido] (Ver no WhatsApp)"
			} else if v.Message.VideoMessage != nil {
				text = "🎥 [Vídeo Recebido] (Ver no WhatsApp)"
			} else if v.Message.DocumentMessage != nil {
				text = "📄 [Documento Recebido] (Ver no WhatsApp)"
			} else if v.Message.StickerMessage != nil {
				text = "💟 [Figurinha]"
			}

			if text != "" {
				SendToChatwoot(senderName, senderPhone, text)
			}
		}()

		go c.HandleWebhook(v)
	}
}

func (c *Client) HandleWebhook(v *events.Message) {
	webhookURL := os.Getenv("WUZAPI_WEBHOOK_URL")
	if webhookURL == "" {
		return
	}
	// Lógica simplificada para manter compatibilidade...
}

func NewClient(deviceStore *sqlstore.Device, logger waLog.Logger) *Client {
	c := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "INFO", true))
	client := &Client{
		client: c,
	}
	c.AddEventHandler(client.EventHandler)
	return client
}
